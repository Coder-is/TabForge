package realtime

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// State is a client connection state reported to OnState.
type State string

const (
	StateConnecting   State = "connecting"
	StateConnected    State = "connected" // a new session started; reload game state
	StateResumed      State = "resumed"   // the previous session continued; nothing was lost
	StateDisconnected State = "disconnected"
	StateClosed       State = "closed" // permanent; see Client.Err
)

// Client is a reconnecting channel client for Go programs: bots, load tests,
// tools and server-to-server links. It mirrors the TypeScript client.
type Client struct {
	URL      string
	Encoding Encoding
	// Token returns the bearer token for a new session (not for resumes).
	Token      func() string
	HTTPHeader http.Header
	// HTTPClient is used for the WebSocket handshake (custom transports/TLS).
	HTTPClient       *http.Client
	MinBackoff       time.Duration
	MaxBackoff       time.Duration
	MaxUnackedFrames int
	DialTimeout      time.Duration
	// Compression offers permessage-deflate; used only if the server agrees.
	Compression          Compression
	CompressionThreshold int
	// OnState is called from the client's goroutine; do not block.
	OnState func(State, *Error)

	contract   *protocol.Contract
	channel    protocol.ResolvedChannel
	codec      *payloadCodec
	schemaHash string

	pushMu   sync.RWMutex
	pushes   map[uint32]func(proto.Message)
	pushNew  map[uint32]func() proto.Message
	dispatch chan func()

	mu          sync.Mutex
	started     bool
	sessionID   string
	resumeToken string
	heartbeat   time.Duration
	nextSeq     uint64
	outbox      []*clientFrame
	peerAck     uint64
	received    uint64
	ackSent     uint64
	pending     map[uint64]*call
	conn        *clientConn
	closed      bool
	err         *Error
	ready       chan struct{}
	readyOnce   sync.Once
	stop        chan struct{}
	stopped     chan struct{}
}

type clientFrame struct {
	outFrame
	written bool
	call    *call
}

type call struct {
	message protocol.ResolvedMessage
	done    chan struct{}
	payload []byte
	err     *Error
}

type clientConn struct {
	ws    wireConn
	queue []*clientFrame
	wake  chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (c *clientConn) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *clientConn) close() {
	c.once.Do(func() {
		close(c.done)
		c.ws.CloseNow()
	})
}

// NewClient creates a client for channel at url: ws:// or wss:// including
// the channel path, or kcp://host:port/path for the KCP transport.
func NewClient(c *protocol.Contract, channel, url string) (*Client, error) {
	ch, ok := c.Channel(channel)
	if !ok {
		return nil, fmt.Errorf("unknown channel %q", channel)
	}
	return &Client{
		URL: url, MinBackoff: 200 * time.Millisecond, MaxBackoff: 10 * time.Second, MaxUnackedFrames: 1024, DialTimeout: 10 * time.Second,
		contract: c, channel: ch, codec: newPayloadCodec(c), schemaHash: c.Fingerprint(),
		pushes: map[uint32]func(proto.Message){}, pushNew: map[uint32]func() proto.Message{},
		dispatch: make(chan func(), 4096), pending: map[uint64]*call{},
		ready: make(chan struct{}), stop: make(chan struct{}), stopped: make(chan struct{}),
	}, nil
}

// OnPush registers an untyped push handler. Handlers run one at a time in
// arrival order on the client's dispatch goroutine.
func (c *Client) OnPush(name string, handler func(proto.Message)) error {
	return onPush[proto.Message](c, name, handler)
}

// HandlePush registers a typed push handler for a generated message type.
func HandlePush[T proto.Message](c *Client, name string, handler func(T)) error {
	return onPush[T](c, name, func(m proto.Message) { handler(m.(T)) })
}

func onPush[T proto.Message](c *Client, name string, handler func(proto.Message)) error {
	m, ok := c.channel.Message(name)
	if !ok || m.Kind != protocol.KindPush || handler == nil {
		return fmt.Errorf("%s: no push message %q", c.channel.ID, name)
	}
	newMessage, err := factory[T](c.codec, m.Input)
	if err != nil {
		return err
	}
	c.pushMu.Lock()
	defer c.pushMu.Unlock()
	c.pushes[m.ID], c.pushNew[m.ID] = handler, newMessage
	return nil
}

// Connect starts the client and waits for the first session.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	if !c.started {
		c.started = true
		go c.run()
		go c.dispatchLoop()
	}
	c.mu.Unlock()
	select {
	case <-c.ready:
		return nil
	case <-c.stopped:
		return c.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Err returns why the client closed permanently.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err == nil {
		return nil
	}
	return c.err
}

// SessionID returns the current session id ("" before the first welcome).
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Close ends the session on the server and stops reconnecting.
func (c *Client) Close() {
	c.shutdown(&Error{Code: CodeClosed, Message: "closed by client"}, true)
	<-c.stopped
}

func (c *Client) shutdown(e *Error, notifyServer bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed, c.err = true, e
	conn := c.conn
	c.conn = nil
	pending := c.pending
	c.pending = map[uint64]*call{}
	started := c.started
	c.mu.Unlock()
	close(c.stop)
	for _, p := range pending {
		p.err = e
		close(p.done)
	}
	if conn != nil {
		if notifyServer {
			data, _ := (&Frame{Kind: KindClose, Error: e}).Encode(c.Encoding)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			conn.ws.Write(ctx, messageType(c.Encoding), data)
			cancel()
		}
		conn.close()
	}
	if !started {
		close(c.stopped)
	}
	c.state(StateClosed, e)
}

func (c *Client) state(s State, e *Error) {
	if c.OnState != nil {
		c.OnState(s, e)
	}
}

// Request sends a request and waits for its response. The contract timeout
// applies unless ctx ends first. A timed-out request may still run on the
// server; its late response is discarded.
func (c *Client) Request(ctx context.Context, name string, request proto.Message) (proto.Message, error) {
	return call_[proto.Message](ctx, c, name, request)
}

// Call is the typed form of Request.
func Call[Resp proto.Message](ctx context.Context, c *Client, name string, request proto.Message) (Resp, error) {
	return call_[Resp](ctx, c, name, request)
}

func call_[Resp proto.Message](ctx context.Context, c *Client, name string, request proto.Message) (Resp, error) {
	var zero Resp
	m, ok := c.channel.Message(name)
	if !ok || m.Kind != protocol.KindRequest {
		return zero, fmt.Errorf("%s: no request message %q", c.channel.ID, name)
	}
	newResponse, err := factory[Resp](c.codec, m.Output)
	if err != nil {
		return zero, err
	}
	payload, err := c.codec.encode(c.Encoding, m.Input, request)
	if err != nil {
		return zero, err
	}
	p := &call{message: m, done: make(chan struct{})}
	seq, err := c.enqueue(&clientFrame{outFrame: outFrame{kind: KindRequest, message: m.ID, payload: payload}, call: p})
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.TimeoutMS)*time.Millisecond)
	defer cancel()
	select {
	case <-p.done:
	case <-ctx.Done():
		c.mu.Lock()
		if c.pending[seq] == p {
			delete(c.pending, seq)
		}
		c.mu.Unlock()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return zero, &Error{Code: CodeTimeout, Message: "request deadline exceeded"}
		}
		return zero, &Error{Code: CodeCancelled, Message: "request cancelled"}
	}
	if p.err != nil {
		return zero, p.err
	}
	response := newResponse()
	if err := c.codec.decode(c.Encoding, p.payload, response); err != nil {
		return zero, &Error{Code: CodeProtocolError, Message: "invalid response: " + err.Error()}
	}
	return response.(Resp), nil
}

// Notify sends a notify message. Reliable notifies are queued while
// disconnected; volatile ones return ErrDropped.
func (c *Client) Notify(name string, message proto.Message) error {
	m, ok := c.channel.Message(name)
	if !ok || m.Kind != protocol.KindNotify {
		return fmt.Errorf("%s: no notify message %q", c.channel.ID, name)
	}
	payload, err := c.codec.encode(c.Encoding, m.Input, message)
	if err != nil {
		return err
	}
	if m.Volatile {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return c.err
		}
		if c.conn == nil || len(c.conn.queue) >= maxVolatileQueued {
			return ErrDropped
		}
		c.conn.queue = append(c.conn.queue, &clientFrame{outFrame: outFrame{kind: KindNotify, message: m.ID, payload: payload}})
		c.conn.signal()
		return nil
	}
	_, err = c.enqueue(&clientFrame{outFrame: outFrame{kind: KindNotify, message: m.ID, payload: payload}})
	return err
}

func (c *Client) enqueue(f *clientFrame) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, c.err
	}
	limit := c.MaxUnackedFrames
	if limit <= 0 {
		limit = 1024
	}
	if len(c.outbox) >= limit {
		return 0, &Error{Code: CodeOverloaded, Message: "too many unacknowledged frames", Retryable: true}
	}
	c.nextSeq++
	f.seq = c.nextSeq
	c.outbox = append(c.outbox, f)
	if f.call != nil {
		c.pending[f.seq] = f.call
	}
	if c.conn != nil {
		c.conn.queue = append(c.conn.queue, f)
		c.conn.signal()
	}
	return f.seq, nil
}

func (c *Client) dispatchLoop() {
	for {
		select {
		case job := <-c.dispatch:
			func() {
				defer func() { recover() }()
				job()
			}()
		case <-c.stop:
			return
		}
	}
}

func (c *Client) run() {
	defer close(c.stopped)
	failures := 0
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		c.state(StateConnecting, nil)
		retryNow, permanent := c.connectOnce()
		if permanent != nil {
			c.shutdown(permanent, false)
			return
		}
		if retryNow {
			failures = 0
			continue
		}
		failures++
		delay := c.MinBackoff << min(failures-1, 16)
		if delay > c.MaxBackoff || delay <= 0 {
			delay = c.MaxBackoff
		}
		delay = delay/2 + time.Duration(rand.Int63n(int64(delay/2)+1))
		select {
		case <-c.stop:
			return
		case <-time.After(delay):
		}
	}
}

// connectOnce runs one transport. retryNow asks for an immediate reconnect
// (the session was replaced); permanent stops the client.
func (c *Client) connectOnce() (retryNow bool, permanent *Error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.DialTimeout)
	defer cancel()
	go func() {
		select {
		case <-c.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ws, err := c.dial(ctx)
	if err != nil {
		return false, nil
	}
	if ws == nil {
		return false, &Error{Code: CodeProtocolError, Message: "server did not accept " + c.Encoding.Subprotocol()}
	}
	typ := messageType(c.Encoding)
	c.mu.Lock()
	hello := &Frame{Kind: KindHello, Ack: c.received, Hello: &Hello{ProtocolVersion: c.contract.Manifest.Version, SchemaHash: c.schemaHash, SessionID: c.sessionID, ResumeToken: c.resumeToken}}
	resuming := c.sessionID != ""
	c.mu.Unlock()
	if !resuming && c.Token != nil {
		hello.Hello.Token = c.Token()
	}
	data, _ := hello.Encode(c.Encoding)
	if err := ws.Write(ctx, typ, data); err != nil {
		ws.CloseNow()
		return false, nil
	}
	got, data, err := ws.Read(ctx)
	if err != nil {
		ws.CloseNow()
		return false, nil
	}
	var first *Frame
	if got == typ {
		first, err = DecodeFrame(c.Encoding, data)
	}
	cancel() // The handshake deadline must not outlive the handshake.
	if first == nil || err != nil || (first.Kind != KindWelcome && first.Kind != KindClose) {
		ws.CloseNow()
		return false, nil
	}
	if first.Kind == KindClose {
		ws.CloseNow()
		if first.Error.Code == CodeSessionExpired && resuming {
			c.resetSession()
			return true, nil
		}
		if first.Error.Retryable {
			return false, nil
		}
		return false, first.Error
	}
	w := first.Welcome
	conn := &clientConn{ws: ws, wake: make(chan struct{}, 1), done: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		ws.CloseNow()
		return false, nil
	}
	if !w.Resumed {
		if resuming {
			c.mu.Unlock()
			c.resetSession()
			c.mu.Lock()
		}
		c.received, c.peerAck = 0, 0
	} else if w.SessionID != c.sessionID {
		c.mu.Unlock()
		ws.CloseNow()
		return false, &Error{Code: CodeProtocolError, Message: "server resumed a different session"}
	}
	if e := c.applyAckLocked(first.Ack); e != nil {
		c.mu.Unlock()
		ws.CloseNow()
		return false, e
	}
	c.sessionID, c.resumeToken = w.SessionID, w.ResumeToken
	c.heartbeat = time.Duration(w.HeartbeatMS) * time.Millisecond
	conn.queue = append([]*clientFrame(nil), c.outbox...)
	c.ackSent = 0
	c.conn = conn
	c.mu.Unlock()
	c.readyOnce.Do(func() { close(c.ready) })
	if w.Resumed {
		c.state(StateResumed, nil)
	} else {
		c.state(StateConnected, nil)
	}
	conn.signal()
	go c.writeLoop(conn)
	go c.pingLoop(conn)
	e := c.readLoop(conn)
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.mu.Unlock()
	conn.close()
	if e != nil {
		if e.Code == CodeSessionExpired {
			c.resetSession()
			return true, nil
		}
		if !e.Retryable {
			return false, e
		}
	}
	c.state(StateDisconnected, e)
	return false, nil
}

// resetSession forgets the old session. Frames never written carry over to
// the next session; requests that may have reached the server fail with
// session_lost because their outcome is unknown.
func (c *Client) resetSession() {
	c.mu.Lock()
	var kept []*clientFrame
	var lost []*call
	for _, f := range c.outbox {
		if !f.written {
			kept = append(kept, f)
			continue
		}
		if f.call != nil {
			delete(c.pending, f.seq)
			lost = append(lost, f.call)
		}
	}
	pending := map[uint64]*call{}
	for i, f := range kept {
		f.seq = uint64(i + 1)
		if f.call != nil {
			pending[f.seq] = f.call
		}
	}
	for _, p := range c.pending {
		found := false
		for _, f := range kept {
			if f.call == p {
				found = true
			}
		}
		if !found {
			lost = append(lost, p) // already timed out callers are not in pending
		}
	}
	c.pending = pending
	c.outbox = kept
	c.nextSeq = uint64(len(kept))
	c.sessionID, c.resumeToken = "", ""
	c.received, c.peerAck, c.ackSent = 0, 0, 0
	c.mu.Unlock()
	for _, p := range lost {
		p.err = &Error{Code: CodeSessionLost, Message: "connection lost; the request may or may not have run", Retryable: false}
		close(p.done)
	}
}

func (c *Client) applyAckLocked(ack uint64) *Error {
	if ack > c.nextSeq {
		return &Error{Code: CodeProtocolError, Message: "server acknowledged a frame that was never sent"}
	}
	if ack <= c.peerAck {
		return nil
	}
	c.peerAck = ack
	n := 0
	for n < len(c.outbox) && c.outbox[n].seq <= ack {
		c.outbox[n] = nil
		n++
	}
	c.outbox = c.outbox[n:]
	if len(c.outbox) == 0 {
		c.outbox = nil
	}
	return nil
}

func (c *Client) writeLoop(conn *clientConn) {
	typ := messageType(c.Encoding)
	for {
		select {
		case <-conn.wake:
		case <-conn.done:
			return
		}
		for {
			c.mu.Lock()
			if c.conn != conn {
				c.mu.Unlock()
				return
			}
			var f *clientFrame
			if len(conn.queue) > 0 {
				f = conn.queue[0]
				conn.queue[0] = nil
				conn.queue = conn.queue[1:]
				if f.seq != 0 && f.seq <= c.peerAck {
					c.mu.Unlock()
					continue
				}
			} else if c.received > c.ackSent {
				f = &clientFrame{outFrame: outFrame{kind: KindAck}}
			} else {
				c.mu.Unlock()
				break
			}
			f.written = true
			frame := Frame{Kind: f.kind, Seq: f.seq, Ack: c.received, Message: f.message, Payload: f.payload}
			c.ackSent = c.received
			c.mu.Unlock()
			data, _ := frame.Encode(c.Encoding)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := conn.ws.Write(ctx, typ, data)
			cancel()
			if err != nil {
				conn.close()
				return
			}
		}
	}
}

func (c *Client) pingLoop(conn *clientConn) {
	c.mu.Lock()
	interval := c.heartbeat
	c.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-conn.done:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn == conn {
				conn.queue = append(conn.queue, &clientFrame{outFrame: outFrame{kind: KindPing}})
				conn.signal()
			}
			c.mu.Unlock()
		}
	}
}

// readLoop returns the server's close error, or nil for a broken transport.
func (c *Client) readLoop(conn *clientConn) *Error {
	typ := messageType(c.Encoding)
	c.mu.Lock()
	idle := 2*c.heartbeat + time.Second
	c.mu.Unlock()
	ackTimer := time.NewTimer(time.Hour)
	ackTimer.Stop()
	defer ackTimer.Stop()
	go func() {
		for {
			select {
			case <-conn.done:
				return
			case <-ackTimer.C:
				conn.signal()
			}
		}
	}()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), idle)
		got, data, err := conn.ws.Read(ctx)
		cancel()
		if err != nil {
			return nil
		}
		if len(data) == 0 {
			continue // Empty messages are not frames.
		}
		var f *Frame
		if got == typ {
			f, err = DecodeFrame(c.Encoding, data)
		}
		if f == nil || err != nil {
			return &Error{Code: CodeProtocolError, Message: "invalid frame from server", Retryable: true}
		}
		if f.Kind == KindClose {
			return f.Error
		}
		c.mu.Lock()
		if e := c.applyAckLocked(f.Ack); e != nil {
			c.mu.Unlock()
			e.Retryable = true
			return e
		}
		var job func()
		switch f.Kind {
		case KindPong, KindAck:
		case KindResponse, KindPush:
			if f.Seq == 0 && f.Kind == KindPush {
				job = c.pushJob(f)
				break
			}
			if f.Seq <= c.received {
				c.ackSent = 0 // Duplicate: acknowledge again.
				ackTimer.Reset(10 * time.Millisecond)
				break
			}
			if f.Seq != c.received+1 {
				c.mu.Unlock()
				return &Error{Code: CodeProtocolError, Message: "sequence gap from server", Retryable: true}
			}
			c.received = f.Seq
			ackTimer.Reset(50 * time.Millisecond)
			if f.Kind == KindPush {
				job = c.pushJob(f)
				break
			}
			if p := c.pending[f.ReplyTo]; p != nil {
				delete(c.pending, f.ReplyTo)
				p.payload, p.err = f.Payload, f.Error
				close(p.done)
			}
		default:
			c.mu.Unlock()
			return &Error{Code: CodeProtocolError, Message: "unexpected " + f.Kind.String() + " frame", Retryable: true}
		}
		c.mu.Unlock()
		if job != nil {
			select {
			case c.dispatch <- job:
			case <-c.stop:
				return nil
			}
		}
	}
}

func (c *Client) pushJob(f *Frame) func() {
	c.pushMu.RLock()
	handler, newMessage := c.pushes[f.Message], c.pushNew[f.Message]
	c.pushMu.RUnlock()
	if handler == nil {
		return nil // Unknown or unhandled push: newer servers may add pushes.
	}
	return func() {
		m := newMessage()
		if err := c.codec.decode(c.Encoding, f.Payload, m); err != nil {
			return
		}
		handler(m)
	}
}

// NewMessage creates an empty dynamic message of a contract type, for
// callers without generated Go types.
func NewMessage(c *protocol.Contract, fullName string) (proto.Message, error) {
	desc, err := c.Files.FindDescriptorByName(protoreflect.FullName(fullName))
	if err != nil {
		return nil, err
	}
	m, ok := desc.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("%s is not a message", fullName)
	}
	return dynamicpb.NewMessage(m), nil
}

// dial opens the transport for c.URL: ws:// or wss:// (WebSocket), or
// kcp://host:port/path (KCP over UDP, see Server.ServeKCP). It returns
// (nil, nil) if the server refused the encoding, and an error for network
// failures (retried with backoff).
func (c *Client) dial(ctx context.Context) (wireConn, error) {
	if strings.HasPrefix(c.URL, "kcp://") {
		conn, err := dialKCP(ctx, c.URL, c.Encoding.Subprotocol(), 1<<20)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) && strings.HasPrefix(ce.Reason, "offer ") {
				return nil, nil
			}
			return nil, err
		}
		return conn, nil
	}
	ws, _, err := websocket.Dial(ctx, c.URL, &websocket.DialOptions{
		Subprotocols:         []string{c.Encoding.Subprotocol()},
		HTTPHeader:           c.HTTPHeader,
		HTTPClient:           c.HTTPClient,
		CompressionMode:      c.Compression.mode(),
		CompressionThreshold: c.CompressionThreshold,
	})
	if err != nil {
		return nil, err
	}
	if ws.Subprotocol() != c.Encoding.Subprotocol() {
		ws.CloseNow()
		return nil, nil
	}
	ws.SetReadLimit(1 << 20)
	return ws, nil
}
