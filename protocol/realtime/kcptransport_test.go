package realtime

import (
	"bytes"
	"context"
	"crypto/ecdh"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"math/rand"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/coder/websocket"
)

// serveKCP starts the fixture's server on a UDP socket.
func serveKCP(t testing.TB, f *fixture) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.server.ServeKCP(pc) }()
	t.Cleanup(func() {
		pc.Close()
		if err := <-done; err != nil {
			t.Errorf("ServeKCP: %v", err)
		}
	})
	return pc.LocalAddr().String()
}

// udpProxy forwards datagrams with loss, duplication and random delay
// (reordering). rebind gives every client a new source port towards the
// server, the way a NAT mapping changes on a mobile network.
type udpProxy struct {
	t                  testing.TB
	conn               *net.UDPConn
	target             *net.UDPAddr
	mu                 sync.Mutex
	r                  *rand.Rand
	loss, dup          float64
	maxDelay           time.Duration
	upstream           map[string]*net.UDPConn
	rebinds, forwarded atomic.Int64
	// corrupt flips one byte in this share of datagrams; watch records
	// whether any datagram contained it (guarded by mu).
	corrupt float64
	watch   []byte
	watched atomic.Bool
}

func newUDPProxy(t testing.TB, target string, loss, dup float64, maxDelay time.Duration) *udpProxy {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := net.ResolveUDPAddr("udp", target)
	p := &udpProxy{t: t, conn: conn, target: addr, r: rand.New(rand.NewSource(13)), loss: loss, dup: dup, maxDelay: maxDelay, upstream: map[string]*net.UDPConn{}}
	go p.serve()
	t.Cleanup(func() {
		conn.Close()
		p.mu.Lock()
		for _, u := range p.upstream {
			u.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *udpProxy) addr() string { return p.conn.LocalAddr().String() }

// forward sends data after a random delay, maybe twice, maybe never.
func (p *udpProxy) forward(write func([]byte)) func([]byte) {
	return func(data []byte) {
		p.mu.Lock()
		copies := 1
		if p.r.Float64() < p.dup {
			copies = 2
		}
		var delays []time.Duration
		for i := 0; i < copies; i++ {
			if p.r.Float64() >= p.loss {
				delays = append(delays, time.Duration(p.r.Int63n(int64(p.maxDelay)+1)))
			}
		}
		if p.watch != nil && bytes.Contains(data, p.watch) {
			p.watched.Store(true)
		}
		flip := -1
		if p.r.Float64() < p.corrupt {
			flip = p.r.Intn(len(data))
		}
		p.mu.Unlock()
		for _, d := range delays {
			b := append([]byte(nil), data...)
			if flip >= 0 {
				b[flip] ^= 0x5A
			}
			time.AfterFunc(d, func() { p.forwarded.Add(1); write(b) })
		}
	}
}

func (p *udpProxy) serve() {
	buf := make([]byte, 64<<10)
	for {
		n, client, err := p.conn.ReadFromUDP(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			continue // Windows: ICMP port unreachable from an earlier send
		}
		up := p.upstreamFor(client)
		if up == nil {
			return
		}
		p.forward(func(b []byte) { up.Write(b) })(buf[:n])
	}
}

func (p *udpProxy) upstreamFor(client *net.UDPAddr) *net.UDPConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if up := p.upstream[client.String()]; up != nil {
		return up
	}
	up, err := net.DialUDP("udp", nil, p.target)
	if err != nil {
		return nil
	}
	p.upstream[client.String()] = up
	back := p.forward(func(b []byte) { p.conn.WriteToUDP(b, client) })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := up.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				continue
			}
			back(buf[:n])
		}
	}()
	return up
}

func (p *udpProxy) rebind() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, up := range p.upstream {
		up.Close()
		delete(p.upstream, key)
	}
	p.rebinds.Add(1)
}

func kcpClient(t testing.TB, f *fixture, enc Encoding, user, addr string) *Client {
	t.Helper()
	c, err := NewClient(f.server.Contract, "game", "kcp://"+addr+"/ws/game")
	if err != nil {
		t.Fatal(err)
	}
	if f.server.KCPKey != nil {
		c.KCPServerKey = f.server.KCPKey.PublicKey()
	}
	c.Encoding = enc
	c.Token = func() string { return "user-" + user }
	c.MinBackoff, c.MaxBackoff = 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(c.Close)
	return c
}

// kcpModes runs a test over clear-text and encrypted KCP, in both encodings.
func kcpModes(t *testing.T, run func(t *testing.T, enc Encoding, key *ecdh.PrivateKey)) {
	for _, encrypted := range []bool{false, true} {
		name := "plain"
		var key *ecdh.PrivateKey
		if encrypted {
			name, key = "encrypted", newKCPKey(t)
		}
		t.Run(name, func(t *testing.T) {
			encodings(t, func(t *testing.T, enc Encoding) { run(t, enc, key) })
		})
	}
}

func newKCPKey(t testing.TB) *ecdh.PrivateKey {
	key, err := ecdh.X25519().GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestKCPRequestPushNotify(t *testing.T) {
	kcpModes(t, func(t *testing.T, enc Encoding, key *ecdh.PrivateKey) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
		c := kcpClient(t, f, enc, "42", serveKCP(t, f))
		chats := make(chan *gamepb.ChatMessage, 1)
		moved := make(chan *gamepb.PlayerMoved, 8)
		HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) { chats <- m })
		HandlePush(c, "playerMoved", func(m *gamepb.PlayerMoved) { moved <- m })
		connect(t, c)
		ctx := context.Background()
		reply, err := Call[*gamepb.LoginReply](ctx, c, "login", &gamepb.LoginRequest{Nickname: "勇者😀"})
		if err != nil || reply.GetPlayerId() != 42 || reply.GetNickname() != "勇者😀" {
			t.Fatalf("login: %v %v", reply, err)
		}
		if _, err := Call[*gamepb.BuyReply](ctx, c, "buy", &gamepb.BuyRequest{ItemId: 3, Count: 1}); err != nil {
			t.Fatal(err)
		}
		if m := <-chats; m.GetMessageId() != 1 {
			t.Fatalf("push: %v", m)
		}
		// A request far larger than one KCP chunk.
		big := strings.Repeat("长消息 big ", 50000)
		if _, err := Call[*gamepb.ChatAck](ctx, c, "chat", &gamepb.ChatSend{Text: big}); err != nil {
			t.Fatalf("large request: %v", err)
		}
		if _, err := c.Request(ctx, "chat", &gamepb.ChatSend{}); err == nil || err.(*Error).Code != "empty_message" {
			t.Fatalf("business error: %v", err)
		}
		deadline := time.After(5 * time.Second)
		for done := false; !done; {
			c.Notify("move", &gamepb.Move{Position: &gamepb.Vec2{X: 1}, Tick: 4})
			select {
			case m := <-moved:
				done = m.GetTick() == 4
			case <-time.After(20 * time.Millisecond):
			case <-deadline:
				t.Fatal("volatile push not delivered")
			}
		}
	})
}

// TestKCPExactlyOnceUnderLossAndRebinds runs concurrent purchases through
// a proxy that drops, duplicates and reorders datagrams and rebinds the
// client's address at random. KCP repairs the losses; rebinds reset the
// conversation and the session resumes on a new one. Encrypted, the proxy
// also corrupts datagrams, which must fail authentication and count as
// lost, and the token must never appear on the wire.
func TestKCPExactlyOnceUnderLossAndRebinds(t *testing.T) {
	kcpModes(t, func(t *testing.T, enc Encoding, key *ecdh.PrivateKey) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
		proxy := newUDPProxy(t, serveKCP(t, f), 0.1, 0.03, 15*time.Millisecond)
		proxy.mu.Lock()
		proxy.watch = []byte("user-7")
		if key != nil {
			proxy.corrupt = 0.02
		}
		proxy.mu.Unlock()
		c := kcpClient(t, f, enc, "7", proxy.addr())
		states := &stateLog{}
		c.OnState = states.record
		var mu sync.Mutex
		var pushes []uint64
		HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) {
			mu.Lock()
			pushes = append(pushes, m.GetMessageId())
			mu.Unlock()
		})
		connect(t, c)
		session := c.SessionID()
		resumed := func() int64 { return int64(states.count(string(StateResumed))) }
		stop := make(chan struct{})
		go func() {
			// One rebind at a time: the next waits until the client has
			// resumed after the last, so a slow machine cannot fold two
			// rebinds into one reconnect.
			r := rand.New(rand.NewSource(17))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(300+r.Intn(400)) * time.Millisecond):
					proxy.rebind()
				}
				for resumed() < proxy.rebinds.Load() {
					select {
					case <-stop:
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
			}
		}()
		// At least 2 s and 3 rebinds; the hard limit only bounds a broken run.
		deadline, limit := time.Now().Add(2*time.Second), time.Now().Add(30*time.Second)
		busy := func() bool {
			now := time.Now()
			return now.Before(limit) && (now.Before(deadline) || proxy.rebinds.Load() < 3)
		}
		results := make(chan int64, 1<<20)
		var wg sync.WaitGroup
		var failures atomic.Int64
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for busy() {
					reply, err := Call[*gamepb.BuyReply](context.Background(), c, "buy", &gamepb.BuyRequest{ItemId: 1, Count: 1})
					if err != nil {
						failures.Add(1)
						t.Errorf("request failed: %v", err)
						return
					}
					results <- reply.GetGold()
				}
			}()
		}
		wg.Wait()
		close(stop)
		close(results)
		if failures.Load() != 0 {
			states.mu.Lock()
			t.Logf("states: %v", states.states)
			states.mu.Unlock()
			return
		}
		var golds []int
		for g := range results {
			golds = append(golds, int(g))
		}
		sort.Ints(golds)
		for i, g := range golds {
			if g != i+1 {
				t.Fatalf("purchases are not exactly 1..%d: %v", len(golds), golds)
			}
		}
		eventually(t, "all pushes", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(pushes) >= len(golds)
		})
		mu.Lock()
		for i, id := range pushes {
			if id != uint64(i+1) {
				t.Fatalf("push %d has id %d", i, id)
			}
		}
		mu.Unlock()
		if c.SessionID() != session || states.count(string(StateConnected)) != 1 {
			t.Fatalf("session restarted: %v", states.states)
		}
		// Every rebind resets the conversation once; the last may come when
		// the workers have stopped, and the idle keepalive notices it.
		eventually(t, "a resume per rebind", func() bool { return resumed() >= proxy.rebinds.Load() })
		resumes := resumed()
		if proxy.rebinds.Load() < 3 {
			t.Fatalf("%d rebinds, %d resumes", proxy.rebinds.Load(), resumes)
		}
		if proxy.watched.Load() != (key == nil) {
			t.Fatalf("token on the wire: %v, encrypted: %v", proxy.watched.Load(), key != nil)
		}
		t.Logf("%d purchases, %d rebinds, %d resumes, %d datagrams forwarded", len(golds), proxy.rebinds.Load(), resumes, proxy.forwarded.Load())
	})
}

func TestKCPRefusals(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	addr := serveKCP(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dialKCP(ctx, "kcp://"+addr+"/nowhere", SubprotocolProto, 1<<20, nil); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("unknown path: %v", err)
	}
	// Client.dial relies on a CloseError to treat a refusal as permanent.
	for i := 0; i < 20; i++ {
		_, err := dialKCP(ctx, "kcp://"+addr+"/ws/game", "chat.v2", 1<<20, nil)
		var ce websocket.CloseError
		if !errors.As(err, &ce) || !strings.HasPrefix(ce.Reason, "offer ") {
			t.Fatalf("unknown subprotocol: %v", err)
		}
	}
	// Datagrams for unknown conversations get a reset, never a new session.
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	segment := make([]byte, 24)
	segment[0], segment[4], segment[12] = 9, 81, 5 // conv 9, push, sn 5
	conn.Write(segment)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || n != 5 || buf[0] != 9 || buf[4] != kcpReset {
		t.Fatalf("reset: %v % x", err, buf[:n])
	}
	// Runts get no answer: a 5-byte reset to a 4-byte spoofed datagram
	// would amplify reflected traffic.
	conn.Write([]byte{9, 0, 0, 0})
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("answered a 4-byte datagram: % x", buf[:n])
	}
}

func TestKCPEncryptionHandshake(t *testing.T) {
	key := newKCPKey(t)
	encrypted := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
	encryptedAddr := serveKCP(t, encrypted)
	plain := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	plainAddr := serveKCP(t, plain)

	// A client pinned to another key never accepts the welcome.
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if _, err := dialKCP(ctx, "kcp://"+encryptedAddr+"/ws/game", SubprotocolProto, 1<<20, newKCPKey(t).PublicKey()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wrong server key: %v", err)
	}

	// Mismatched configurations stop the client instead of retrying.
	for _, tc := range []struct {
		name, addr string
		key        *ecdh.PublicKey
		want       error
	}{
		{"plain client", encryptedAddr, nil, ErrKCPEncryptionRequired},
		{"encrypting client", plainAddr, key.PublicKey(), ErrKCPEncryptionUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := kcpClient(t, plain, EncodingProto, "1", tc.addr)
			c.KCPServerKey = tc.key
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := c.Connect(ctx)
			var e *Error
			if !errors.As(err, &e) || e.Code != CodeProtocolError || e.Message != tc.want.Error() {
				t.Fatalf("Connect: %v", err)
			}
		})
	}

	// A repeated hello (its welcome was lost) gets the same welcome, which
	// is never larger than the hello; a P-256 key is refused up front.
	conn, err := net.Dial("udp", encryptedAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	h, err := newKCPClientHandshake(77, key.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	var welcomes [2][]byte
	for i := range welcomes {
		conn.Write(h.hello)
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 256)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		welcomes[i] = buf[:n]
	}
	if !bytes.Equal(welcomes[0], welcomes[1]) || len(welcomes[0]) > len(h.hello) || h.finish(welcomes[0]) == nil {
		t.Fatalf("welcomes % x / % x", welcomes[0], welcomes[1])
	}
	// The welcome's tag binds it to the server key: a client pinned to
	// another key rejects it outright instead of timing out on data later.
	wrong, err := newKCPClientHandshake(77, newKCPKey(t).PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	wrong.eph = h.eph
	if wrong.finish(welcomes[0]) != nil {
		t.Fatal("accepted a welcome from a server without the pinned key")
	}
	p256, err := ecdh.P256().GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newKCPClientHandshake(1, p256.PublicKey()); !errors.Is(err, errKCPKeyCurve) {
		t.Fatalf("P-256 key: %v", err)
	}
	if err := (&Server{KCPKey: p256}).ServeKCP(nil); !errors.Is(err, errKCPKeyCurve) {
		t.Fatalf("ServeKCP with a P-256 key: %v", err)
	}
}

// TestKCPHandshakeSurvivesDamage covers handshakes whose datagrams were
// damaged on the way: a corrupted hello must not leave the server holding
// a conversation the client can never complete, and a reset or two (a
// damaged hello, or a forged reset) must not make the client give up.
func TestKCPHandshakeSurvivesDamage(t *testing.T) {
	key := newKCPKey(t)
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
	addr := serveKCP(t, f)
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func() []byte {
		t.Helper()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 256)
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		return buf[:n]
	}
	h, err := newKCPClientHandshake(91, key.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), h.hello...)
	damaged[10] ^= 0x5A // inside the client's key
	conn.Write(damaged)
	if h.finish(read()) != nil {
		t.Fatal("the welcome to a damaged hello completed the handshake")
	}
	conn.Write(h.hello)
	if h.finish(read()) == nil {
		t.Fatal("the repeated hello did not replace the damaged one")
	}

	// A fake server resets the first two hellos, then welcomes.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 256)
		for i := 0; ; i++ {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			conv := binary.LittleEndian.Uint32(buf)
			if i < kcpHandshakeResets-1 {
				pc.WriteTo(kcpResetDatagram(conv), from)
				continue
			}
			_, welcome, err := kcpAccept(key, conv, buf[:n])
			if err == nil {
				pc.WriteTo(welcome, from)
			}
		}
	}()
	udp, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := kcpHandshake(ctx, udp, 92, key.PublicKey()); err != nil {
		t.Fatalf("handshake after %d resets: %v", kcpHandshakeResets-1, err)
	}
}

// TestKCPKeyRotation: while rotating, clients pinned to the new key and to
// a previous one both connect; once the old key is gone its clients cannot.
func TestKCPKeyRotation(t *testing.T) {
	oldKey, newKey := newKCPKey(t), newKCPKey(t)
	rotating := newFixture(t, testContract(t, nil), func(s *Server) {
		registerGame(t, s)
		s.KCPKey, s.KCPPreviousKeys = newKey, []*ecdh.PrivateKey{oldKey}
	})
	rotated := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = newKey })
	rotatingAddr, rotatedAddr := serveKCP(t, rotating), serveKCP(t, rotated)
	for _, tc := range []struct {
		name string
		addr string
		pin  *ecdh.PrivateKey
		ok   bool
	}{
		{"new key while rotating", rotatingAddr, newKey, true},
		{"old key while rotating", rotatingAddr, oldKey, true},
		{"old key after rotation", rotatedAddr, oldKey, false},
		{"new key after rotation", rotatedAddr, newKey, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := kcpClient(t, rotating, EncodingProto, "3", tc.addr)
			c.KCPServerKey = tc.pin.PublicKey()
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			err := c.Connect(ctx)
			if tc.ok && err != nil {
				t.Fatalf("Connect: %v", err)
			}
			if !tc.ok && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("connected with a removed key: %v", err)
			}
			if tc.ok {
				if _, err := Call[*gamepb.LoginReply](context.Background(), c, "login", &gamepb.LoginRequest{Nickname: "x"}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if err := (&Server{KCPPreviousKeys: []*ecdh.PrivateKey{oldKey}}).ServeKCP(nil); err == nil {
		t.Fatal("ServeKCP accepted previous keys without a current one")
	}
}

// linkKCP connects two conversations through in-memory queues that drop
// the given share of datagrams; with sched, it drives both clocks.
func linkKCP(t testing.TB, maxMessage int, loss float64, sched *kcpScheduler) (a, b *kcpConn) {
	toA, toB := make(chan []byte, 4096), make(chan []byte, 4096)
	var mu sync.Mutex
	r := rand.New(rand.NewSource(1))
	deliver := func(ch chan []byte) func([]byte) {
		return func(p []byte) {
			mu.Lock()
			drop := r.Float64() < loss
			mu.Unlock()
			if drop {
				return
			}
			select {
			case ch <- append([]byte(nil), p...):
			default: // A full link drops like a full UDP buffer.
			}
		}
	}
	a = newKCPConn(7, maxMessage, nil, deliver(toB), sched)
	b = newKCPConn(7, maxMessage, nil, deliver(toA), sched)
	done := make(chan struct{})
	pump := func(ch chan []byte, c *kcpConn) {
		for {
			select {
			case p := <-ch:
				c.receive(p)
			case <-done:
				return
			}
		}
	}
	go pump(toA, a)
	go pump(toB, b)
	t.Cleanup(func() {
		close(done)
		a.fail(net.ErrClosed)
		b.fail(net.ErrClosed)
	})
	return a, b
}

// An unread conversation buffers about one maxMessage of complete messages,
// not kcpInboxLimit full-size ones; reading drains the rest in order.
func TestKCPInboxBoundedByBytes(t *testing.T) {
	const maxMessage, size, count = 64 << 10, 60 << 10, 40
	a, b := linkKCP(t, maxMessage, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() {
		for i := 0; i < count; i++ {
			if err := a.send(ctx, kcpBinary, bytes.Repeat([]byte{byte(i)}, size), true); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	time.Sleep(time.Second)
	b.mu.Lock()
	held, n := 0, len(b.inbox)
	for _, m := range b.inbox {
		held += len(m.data)
	}
	b.mu.Unlock()
	if held > 2*maxMessage {
		t.Fatalf("unread inbox holds %d bytes in %d messages", held, n)
	}
	for i := 0; i < count; i++ {
		_, data, err := b.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != size || data[0] != byte(i) {
			t.Fatalf("message %d: %d bytes starting %d", i, len(data), data[0])
		}
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
}

// Spoofed hellos only fill the pending pool: it is capped per IP, a
// conversation leaves it once the client proves it got the welcome, and
// conversations that never do expire.
func TestKCPPendingPool(t *testing.T) {
	old := kcpPendingTimeout
	kcpPendingTimeout = 300 * time.Millisecond
	t.Cleanup(func() { kcpPendingTimeout = old })
	key := newKCPKey(t)
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	l, err := newKCPListener(f.server, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(l.closeAll)
	counts := func(ip netip.Addr) (perIP, total, conns int) {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.pending[ip], l.pendingTotal, len(l.conns)
	}
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	// A flood of hellos from one address, repeated the way clients repeat
	// them, never holds more than MaxKCPPendingPerIP conversations.
	flooder := netip.MustParseAddrPort("127.0.0.1:40000")
	hellos := make([][]byte, 3*MaxKCPPendingPerIP)
	for i := range hellos {
		h, err := newKCPClientHandshake(uint32(i+1), key.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		hellos[i] = h.hello
	}
	for round := 0; round < 20; round++ {
		for i, hello := range hellos {
			l.find(kcpKey{flooder, uint32(i + 1)}, hello)
		}
		if perIP, _, _ := counts(flooder.Addr()); perIP > MaxKCPPendingPerIP {
			t.Fatalf("%d pending conversations from one address", perIP)
		}
		time.Sleep(2 * time.Millisecond)
	}
	waitFor("the per-IP cap to fill", func() bool { n, _, _ := counts(flooder.Addr()); return n == MaxKCPPendingPerIP })

	// Once the flood expires, a real client's handshake is pending until its
	// first sealed datagram opens, and is then established for good.
	client, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	addr := client.LocalAddr().(*net.UDPAddr).AddrPort()
	h, err := newKCPClientHandshake(77, key.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	real := kcpKey{addr, 77}
	waitFor("the flood to expire", func() bool { _, total, conns := counts(flooder.Addr()); return total == 0 && conns == 0 })
	var welcome []byte
	buf := make([]byte, 128)
	for welcome == nil {
		l.find(real, h.hello)
		client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, _, err := client.ReadFrom(buf); err == nil {
			welcome = append([]byte(nil), buf[:n]...)
		}
	}
	if perIP, _, _ := counts(addr.Addr()); perIP != 1 {
		t.Fatalf("handshake not pending: %d", perIP)
	}
	cipher := h.finish(welcome)
	if cipher == nil {
		t.Fatal("bad welcome")
	}
	c := l.find(real, cipher.seal(nil))
	if c == nil {
		t.Fatal("conversation lost")
	}
	c.receive(cipher.seal(nil))
	if perIP, total, _ := counts(addr.Addr()); perIP != 0 || total != 0 {
		t.Fatalf("established conversation still pending: %d/%d", perIP, total)
	}
	time.Sleep(2 * kcpPendingTimeout)
	if _, _, conns := counts(addr.Addr()); conns != 1 {
		t.Fatal("established conversation expired like a pending one")
	}
}

// Frames written with WriteBatched share datagrams until Flush.
func TestKCPWriteBatchedSharesDatagrams(t *testing.T) {
	ctx := context.Background()
	msg := bytes.Repeat([]byte{1}, 64)
	count := func(batched bool) int {
		var datagrams atomic.Int64
		c := newKCPConn(5, 1<<20, nil, func([]byte) { datagrams.Add(1) }, nil)
		defer c.CloseNow()
		for i := 0; i < 50; i++ {
			write := c.Write
			if batched && i < 49 {
				write = c.WriteBatched
			}
			if err := write(ctx, websocket.MessageBinary, msg); err != nil {
				t.Fatal(err)
			}
		}
		c.Flush()
		return int(datagrams.Load())
	}
	single, batched := count(false), count(true)
	// About 4 MTU-sized datagrams; a KCP tick during the loop may add one or two.
	if single < 50 || batched > 10 {
		t.Fatalf("50 frames: %d datagrams one by one, %d batched", single, batched)
	}
}

// Scheduled conversations repair losses, leave the scheduler once KCP has
// nothing to do, and come back when there is traffic again.
func TestKCPSchedulerDrivesConversations(t *testing.T) {
	sched := newKCPScheduler()
	defer sched.close()
	a, b := linkKCP(t, 1<<20, 0.2, sched)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transfer := func(from, count int) {
		t.Helper()
		sent := make(chan error, 1)
		go func() {
			for i := from; i < from+count; i++ {
				msg := bytes.Repeat([]byte{byte(i)}, 1+(i*397)%3000)
				if err := a.send(ctx, kcpBinary, msg, i%3 == 0); err != nil {
					sent <- err
					return
				}
			}
			a.Flush()
			sent <- nil
		}()
		for i := from; i < from+count; i++ {
			_, data, err := b.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != 1+(i*397)%3000 || data[0] != byte(i) {
				t.Fatalf("message %d: %d bytes starting %d", i, len(data), data[0])
			}
		}
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
	}
	idle := func() bool {
		active := 0
		for i := range sched.shards {
			sh := &sched.shards[i]
			sh.mu.Lock()
			active += len(sh.active)
			sh.mu.Unlock()
		}
		return active == 0
	}
	waitIdle := func() {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !idle(); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("conversations stay scheduled after the transfer")
			}
		}
	}
	transfer(0, 300)
	waitIdle()
	time.Sleep(300 * time.Millisecond) // KCP's clock is not advanced while idle.
	transfer(300, 20)
	waitIdle()
}
