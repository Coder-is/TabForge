package realtime

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol/realtime/internal/kcp"
	"github.com/coder/websocket"
)

// wireConn is the message transport under a session: a WebSocket, or a KCP
// conversation over UDP (*websocket.Conn implements it directly).
type wireConn interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	Close(code websocket.StatusCode, reason string) error
	CloseNow() error
}

// KCP transport, version 1. Every KCP message starts with a header byte:
// the low 7 bits are the type, 0x80 means another chunk of the same
// message follows. The client opens with "path\nsubprotocol"; the server
// answers with the accepted subprotocol, or with close. Data chunks carry
// WebSocket-equivalent messages; close carries the reason code.
//
// A datagram of 5 bytes, conv followed by 0xFF, resets a conversation: the
// server sends it for a conversation it does not know (after a NAT rebind
// or a restart), so the client reconnects without waiting for timeouts.
// A client that has heard nothing for a second sends conv followed by 0xFE
// (keepalive); the server ignores it for a known conversation and resets an
// unknown one, so a changed NAT mapping is noticed even when idle.
const (
	kcpOpen        = 0
	kcpBinary      = 1
	kcpText        = 2
	kcpClose       = 3
	kcpMore        = 0x80
	kcpChunk       = 16 << 10
	kcpReset       = 0xFF
	kcpKeepalive   = 0xFE
	kcpQuiet       = time.Second // client keepalive after this much silence
	kcpMTU         = 1200
	kcpWindow      = 256
	kcpInterval    = 10 * time.Millisecond
	kcpSendLimit   = 2 * kcpWindow // queued segments before Write blocks
	kcpInboxLimit  = 64            // complete messages buffered before KCP's window pushes back
	kcpCloseLinger = time.Second
)

// ErrKCPReset reports that the peer no longer knows the conversation.
var ErrKCPReset = errors.New("realtime: kcp conversation reset by peer")

type kcpMessage struct {
	typ  byte
	data []byte
}

type kcpConn struct {
	mu         sync.Mutex
	kcp        *kcp.KCP
	start      time.Time
	maxMessage int
	inbox      []kcpMessage
	partial    []byte
	partialTyp byte
	partialOn  bool
	opened     chan kcpMessage // first message (open or close), once
	openSeen   bool            // the peer sent its open message
	readable   chan struct{}
	writable   chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
	err        error
	onClose    func()
	// keepalive is set on clients: it sends a raw keepalive datagram.
	keepalive func()
	lastRecv  time.Time
	lastAlive time.Time
	// client is set on dialed conversations; only clients act on plain refusals.
	client bool
	// crypt seals and opens every datagram when the conversation is encrypted.
	crypt *kcpCipher
	// raw sends a datagram as is. Servers keep the client's hello and their
	// welcome to answer a repeated hello whose welcome was lost.
	raw            func([]byte)
	hello, welcome []byte
}

// newKCPConn starts a conversation that sends datagrams with raw; with
// crypt they are sealed first (and KCP segments shrink to fit the MTU).
func newKCPConn(conv uint32, maxMessage int, crypt *kcpCipher, raw func([]byte)) *kcpConn {
	c := &kcpConn{
		start: time.Now(), maxMessage: maxMessage, opened: make(chan kcpMessage, 1),
		readable: make(chan struct{}, 1), writable: make(chan struct{}, 1), closed: make(chan struct{}),
		crypt: crypt, raw: raw,
	}
	output, mtu := raw, kcpMTU
	if crypt != nil {
		output, mtu = func(b []byte) { raw(crypt.seal(b)) }, kcpMTU-kcpSealOverhead
	}
	c.kcp = kcp.New(conv, output)
	c.kcp.NoDelay(1, int(kcpInterval/time.Millisecond), 2, true)
	c.kcp.WndSize(kcpWindow, kcpWindow)
	c.kcp.SetMTU(mtu)
	c.kcp.SetDeadLink(40)
	c.kcp.Update(0)
	go c.tick()
	return c
}

func (c *kcpConn) now() uint32 { return uint32(time.Since(c.start).Milliseconds()) }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *kcpConn) tick() {
	t := time.NewTicker(kcpInterval)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
		}
		c.mu.Lock()
		c.kcp.Update(c.now())
		dead := c.kcp.Dead()
		quiet := c.keepalive != nil && time.Since(c.lastRecv) > kcpQuiet && time.Since(c.lastAlive) > kcpQuiet
		if quiet {
			c.lastAlive = time.Now()
		}
		if c.kcp.WaitSnd() < kcpSendLimit {
			signal(c.writable)
		}
		c.mu.Unlock()
		if quiet {
			c.keepalive()
		}
		if dead {
			c.fail(errors.New("realtime: kcp peer stopped acknowledging"))
		}
	}
}

// receive handles one datagram from the peer; it does not retain data.
// A reset closes the conversation; on encrypted conversations everything
// else must be sealed, and what fails to open is dropped like noise.
func (c *kcpConn) receive(data []byte) {
	if len(data) == 5 && data[4] == kcpReset {
		c.fail(ErrKCPReset)
		return
	}
	if c.crypt == nil {
		switch {
		case len(data) == 5 && data[4] == kcpKeepalive:
			return // Proves the address is current; nothing else to do.
		case len(data) == 5 && data[4] == kcpPlainRefuse && c.client:
			c.fail(ErrKCPEncryptionRequired)
			return
		}
		c.input(append([]byte(nil), data...))
		return
	}
	if c.welcome != nil && bytes.Equal(data, c.hello) {
		c.raw(c.welcome) // The client did not get the welcome yet.
		return
	}
	plain, ok := c.crypt.open(data)
	if !ok || len(plain) == 0 { // Empty: keepalive.
		return
	}
	c.input(plain)
}

// input feeds one KCP datagram.
func (c *kcpConn) input(data []byte) {
	c.mu.Lock()
	c.lastRecv = time.Now()
	ok := c.kcp.Input(data)
	var err error
	if ok {
		err = c.pumpLocked()
		c.kcp.Flush() // Acknowledge at once instead of on the next tick.
	}
	c.mu.Unlock()
	if err != nil {
		c.fail(err)
	}
}

// pumpLocked moves complete KCP messages into the inbox while it has room;
// leaving the rest in KCP lets its receive window slow the sender down.
func (c *kcpConn) pumpLocked() error {
	for len(c.inbox) < kcpInboxLimit {
		m := c.kcp.Recv()
		if m == nil {
			return nil
		}
		if len(m) == 0 {
			return errors.New("realtime: empty kcp message")
		}
		typ, more := m[0]&^kcpMore, m[0]&kcpMore != 0
		switch typ {
		case kcpOpen:
			if c.openSeen {
				return errors.New("realtime: unexpected kcp open")
			}
			c.openSeen = true
			c.opened <- kcpMessage{typ: typ, data: m[1:]}
		case kcpClose:
			reason := string(m[1:])
			select {
			case c.opened <- kcpMessage{typ: typ, data: m[1:]}:
			default:
			}
			return websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: reason}
		case kcpBinary, kcpText:
			if c.partialOn && c.partialTyp != typ {
				return errors.New("realtime: kcp message changed type mid-way")
			}
			c.partialOn, c.partialTyp = true, typ
			if len(c.partial)+len(m)-1 > c.maxMessage {
				return fmt.Errorf("realtime: kcp message exceeds %d bytes", c.maxMessage)
			}
			c.partial = append(c.partial, m[1:]...)
			if !more {
				c.inbox = append(c.inbox, kcpMessage{typ: typ, data: c.partial})
				c.partial, c.partialOn = nil, false
				signal(c.readable)
			}
		default:
			return fmt.Errorf("realtime: unknown kcp message type %d", typ)
		}
	}
	return nil
}

func (c *kcpConn) fail(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.closed)
		if c.onClose != nil {
			c.onClose()
		}
	})
}

func (c *kcpConn) closedErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	return net.ErrClosed
}

// send queues one transport message, waiting while too much is unacknowledged.
func (c *kcpConn) send(ctx context.Context, typ byte, p []byte) error {
	for {
		c.mu.Lock()
		if c.kcp.WaitSnd() < kcpSendLimit {
			break
		}
		c.mu.Unlock()
		select {
		case <-c.writable:
		case <-c.closed:
			return c.closedErr()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer c.mu.Unlock()
	select {
	case <-c.closed:
		return net.ErrClosed
	default:
	}
	for first := true; first || len(p) > 0; first = false {
		n := min(len(p), kcpChunk)
		header := typ
		if n < len(p) {
			header |= kcpMore
		}
		chunk := make([]byte, 1+n)
		chunk[0] = header
		copy(chunk[1:], p[:n])
		if !c.kcp.Send(chunk) {
			return errors.New("realtime: kcp chunk too large")
		}
		p = p[n:]
	}
	c.kcp.Flush()
	return nil
}

func (c *kcpConn) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	for {
		c.mu.Lock()
		if len(c.inbox) > 0 {
			m := c.inbox[0]
			c.inbox = c.inbox[1:]
			err := c.pumpLocked()
			c.mu.Unlock()
			if err != nil {
				c.fail(err)
			}
			if m.typ == kcpText {
				return websocket.MessageText, m.data, nil
			}
			return websocket.MessageBinary, m.data, nil
		}
		c.mu.Unlock()
		select {
		case <-c.readable:
		case <-c.closed:
			return 0, nil, c.closedErr()
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		}
	}
}

func (c *kcpConn) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	if len(p) > c.maxMessage {
		return fmt.Errorf("realtime: message of %d bytes exceeds %d", len(p), c.maxMessage)
	}
	t := byte(kcpBinary)
	if typ == websocket.MessageText {
		t = kcpText
	}
	return c.send(ctx, t, p)
}

// Close sends the reason and waits briefly for it to be acknowledged.
func (c *kcpConn) Close(_ websocket.StatusCode, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), kcpCloseLinger)
	defer cancel()
	if err := c.send(ctx, kcpClose, []byte(reason)); err == nil {
		for ctx.Err() == nil {
			c.mu.Lock()
			pending := c.kcp.WaitSnd()
			c.mu.Unlock()
			if pending == 0 {
				break
			}
			select {
			case <-c.closed:
				return nil
			case <-time.After(kcpInterval):
			}
		}
	}
	c.fail(net.ErrClosed)
	return nil
}

func (c *kcpConn) CloseNow() error {
	c.fail(net.ErrClosed)
	return nil
}

// isOpenSegment reports whether a datagram starts a conversation: its first
// segment is the first data segment (sn 0).
func isOpenSegment(data []byte) bool {
	return len(data) >= kcp.Overhead && data[4] == 81 && binary.LittleEndian.Uint32(data[12:]) == 0
}

func kcpResetDatagram(conv uint32) []byte {
	b := make([]byte, 5)
	binary.LittleEndian.PutUint32(b, conv)
	b[4] = kcpReset
	return b
}

// ServeKCP serves the channels over KCP on pc (for example a UDP socket
// from net.ListenPacket) until pc is closed. It can run next to the
// WebSocket handler; sessions are shared, so a client may resume a session
// over either transport with the same encoding.
//
// Without Server.KCPKey, KCP datagrams are not encrypted: tokens and
// payloads travel in clear text, unlike wss://. With it, ServeKCP accepts
// only encrypted conversations. See doc/realtime.md.
func (s *Server) ServeKCP(pc net.PacketConn) error {
	if s.KCPKey != nil && s.KCPKey.Curve() != ecdh.X25519() {
		return errKCPKeyCurve
	}
	l := &kcpListener{srv: s, pc: pc, key: s.KCPKey, conns: map[string]*kcpConn{}}
	buf := make([]byte, 64<<10)
	failures := 0
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				l.closeAll()
				return nil
			}
			// Read errors on a UDP socket are usually about one peer, not
			// the socket: Windows reports an ICMP "port unreachable" for an
			// earlier send to a departed client as a read error. Keep
			// serving unless the socket keeps failing.
			if failures++; failures > 1000 {
				l.closeAll()
				return err
			}
			continue
		}
		failures = 0
		data := buf[:n]
		if n < 5 { // Shorter than a keepalive; a reset reply would amplify spoofed traffic.
			continue
		}
		// Not kcp.Conv: 5-byte keepalives must reach find, which resets
		// conversations it does not know.
		conv := binary.LittleEndian.Uint32(data)
		if c := l.find(addr, conv, data); c != nil {
			c.receive(data)
		}
	}
}

// MaxKCPConversations bounds concurrent KCP conversations per ServeKCP.
const MaxKCPConversations = 100000

type kcpListener struct {
	srv   *Server
	pc    net.PacketConn
	key   *ecdh.PrivateKey
	mu    sync.Mutex
	conns map[string]*kcpConn
}

// find returns the conversation a datagram belongs to, starting one for a
// plain open segment (or a hello, when encrypted). It answers datagrams
// for unknown conversations with a reset, or with a plain refusal when a
// plain client meets an encrypting server, but never answers those two.
func (l *kcpListener) find(addr net.Addr, conv uint32, data []byte) *kcpConn {
	key := fmt.Sprintf("%s/%d", addr, conv)
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := l.conns[key]; c != nil {
		return c
	}
	var crypt *kcpCipher
	var welcome []byte
	switch {
	case len(data) == 5 && (data[4] == kcpReset || data[4] == kcpPlainRefuse):
		return nil
	case l.key == nil && isOpenSegment(data):
	case l.key != nil && isOpenSegment(data):
		refuse := kcpResetDatagram(conv)
		refuse[4] = kcpPlainRefuse
		l.pc.WriteTo(refuse, addr)
		return nil
	case l.key != nil && isKCPHello(data):
		if len(l.conns) >= MaxKCPConversations {
			return nil
		}
		var err error
		if crypt, welcome, err = kcpAccept(l.key, conv, data); err != nil {
			return nil
		}
	default:
		l.pc.WriteTo(kcpResetDatagram(conv), addr)
		return nil
	}
	if len(l.conns) >= MaxKCPConversations {
		return nil
	}
	limit := l.srv.MaxFrameBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	c := newKCPConn(conv, limit, crypt, func(b []byte) { l.pc.WriteTo(b, addr) })
	if crypt != nil {
		// receive answers this hello, and repeats of it, with the welcome.
		c.hello, c.welcome = append([]byte(nil), data...), welcome
	}
	c.onClose = func() {
		l.mu.Lock()
		if l.conns[key] == c {
			delete(l.conns, key)
		}
		l.mu.Unlock()
	}
	l.conns[key] = c
	go l.srv.serveKCPConn(c)
	return c
}

func (l *kcpListener) closeAll() {
	l.mu.Lock()
	conns := make([]*kcpConn, 0, len(l.conns))
	for _, c := range l.conns {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		c.CloseNow()
	}
}

func (s *Server) serveKCPConn(c *kcpConn) {
	ctx, cancel := context.WithTimeout(context.Background(), s.handshakeTimeout())
	defer cancel()
	var open kcpMessage
	select {
	case open = <-c.opened:
	case <-c.closed:
		return
	case <-ctx.Done():
		c.CloseNow()
		return
	}
	if open.typ != kcpOpen {
		c.CloseNow()
		return
	}
	path, subprotocol, _ := strings.Cut(string(open.data), "\n")
	var ch *channelState
	for _, candidate := range s.channels {
		if candidate.Path == path {
			ch = candidate
		}
	}
	enc, ok := encodingFor(subprotocol)
	s.mu.RLock()
	closing := s.closing
	s.mu.RUnlock()
	switch {
	case ch == nil:
		c.Close(websocket.StatusPolicyViolation, "not_found")
		return
	case !ok:
		c.Close(websocket.StatusProtocolError, "offer "+SubprotocolProto+" or "+SubprotocolJSON)
		return
	case closing:
		c.Close(websocket.StatusGoingAway, CodeServerShutdown)
		return
	}
	if err := c.send(ctx, kcpOpen, []byte(subprotocol)); err != nil {
		c.CloseNow()
		return
	}
	s.wg.Add(1)
	defer s.wg.Done()
	s.serveConn(context.Background(), ch, c, enc)
}

// dialKCP opens a KCP conversation to kcp://host:port/path, encrypted when
// serverKey is set.
func dialKCP(ctx context.Context, rawURL, subprotocol string, maxMessage int, serverKey *ecdh.PublicKey) (*kcpConn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	udp, err := d.DialContext(ctx, "udp", u.Host)
	if err != nil {
		return nil, err
	}
	var b [4]byte
	conv := uint32(0)
	for conv == 0 {
		if _, err := rand.Read(b[:]); err != nil {
			udp.Close()
			return nil, err
		}
		conv = binary.LittleEndian.Uint32(b[:])
	}
	var crypt *kcpCipher
	if serverKey != nil {
		if crypt, err = kcpHandshake(ctx, udp, conv, serverKey); err != nil {
			udp.Close()
			return nil, err
		}
	}
	c := newKCPConn(conv, maxMessage, crypt, func(p []byte) { udp.Write(p) })
	alive := kcpResetDatagram(conv)
	alive[4] = kcpKeepalive
	keepalive := func() { udp.Write(alive) }
	if crypt != nil {
		keepalive = func() { udp.Write(crypt.seal(nil)) }
	}
	c.mu.Lock()
	c.client, c.lastRecv, c.keepalive = true, time.Now(), keepalive
	c.mu.Unlock()
	c.onClose = func() { udp.Close() }
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := udp.Read(buf)
			if err != nil {
				c.fail(err)
				return
			}
			if n >= 5 && binary.LittleEndian.Uint32(buf) == conv {
				c.receive(buf[:n])
			}
		}
	}()
	path := u.Path
	if path == "" {
		path = "/"
	}
	if err := c.send(ctx, kcpOpen, []byte(path+"\n"+subprotocol)); err != nil {
		c.CloseNow()
		return nil, err
	}
	select {
	case m := <-c.opened:
		if m.typ == kcpClose {
			// The same error the closed branch returns, so Client.dial can
			// tell a refusal from a network failure whichever branch wins.
			c.CloseNow()
			return nil, websocket.CloseError{Code: websocket.StatusNormalClosure, Reason: string(m.data)}
		}
		if m.typ != kcpOpen || string(m.data) != subprotocol {
			c.CloseNow()
			return nil, fmt.Errorf("realtime: kcp open refused: %s", m.data)
		}
		return c, nil
	case <-c.closed:
		return nil, c.closedErr()
	case <-ctx.Done():
		c.CloseNow()
		return nil, ctx.Err()
	}
}

// kcpHelloRetry is how long the client waits for a welcome before
// repeating its hello.
const kcpHelloRetry = 250 * time.Millisecond

// kcpHandshake runs the client's side of the encrypted handshake on udp
// before any KCP traffic. Datagrams that are not a genuine welcome (from a
// wrong key or a forger) are ignored; a reset means the server does not
// encrypt, since an encrypting server answers every hello.
func kcpHandshake(ctx context.Context, udp net.Conn, conv uint32, server *ecdh.PublicKey) (*kcpCipher, error) {
	h, err := newKCPClientHandshake(conv, server)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { udp.SetReadDeadline(time.Now()) })
	defer stop()
	defer udp.SetReadDeadline(time.Time{})
	buf := make([]byte, 2048)
	for {
		if _, err := udp.Write(h.hello); err != nil {
			return nil, err
		}
		udp.SetReadDeadline(time.Now().Add(kcpHelloRetry))
		for {
			n, err := udp.Read(buf)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				break // Repeat the hello.
			}
			if err != nil {
				return nil, err
			}
			data := buf[:n]
			if n == 5 && binary.LittleEndian.Uint32(data) == conv && data[4] == kcpReset {
				return nil, ErrKCPEncryptionUnsupported
			}
			if k := h.finish(data); k != nil {
				return k, nil
			}
		}
	}
}
