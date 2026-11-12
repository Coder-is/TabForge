package realtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// ErrSessionClosed is returned when pushing to an ended session.
var ErrSessionClosed = errors.New("realtime: session closed")

// ErrDropped reports a volatile push that was not queued because the session
// is disconnected or its send queue is full. It is expected, not a failure.
var ErrDropped = errors.New("realtime: volatile message dropped")

// frameOverhead is a generous bound on envelope bytes around a payload.
const frameOverhead = 128

// maxVolatileQueued bounds volatile frames waiting on one connection.
const maxVolatileQueued = 256

type outFrame struct {
	kind    Kind
	seq     uint64
	message uint32
	replyTo uint64
	payload []byte
	err     *Error
	welcome *Welcome
	size    int
}

type connection struct {
	ws       wireConn
	queue    []*outFrame // guarded by Session.mu
	volatile int         // queued volatile frames, guarded by Session.mu
	wake     chan struct{}
	done     chan struct{}
	once     sync.Once
	tokens   float64
	last     time.Time
}

func newConnection(ws wireConn) *connection {
	return &connection{ws: ws, wake: make(chan struct{}, 1), done: make(chan struct{})}
}

func (c *connection) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// close tears the connection down without a close frame.
func (c *connection) close() {
	c.once.Do(func() {
		close(c.done)
		c.ws.CloseNow()
	})
}

// closeWith sends a close frame first so the peer learns why.
func (c *connection) closeWith(enc Encoding, e *Error, timeout time.Duration) {
	c.once.Do(func() {
		close(c.done)
		data, _ := (&Frame{Kind: KindClose, Error: e}).Encode(enc)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if c.ws.Write(ctx, messageType(enc), data) == nil {
			c.ws.Close(websocket.StatusNormalClosure, e.Code)
		}
		c.ws.CloseNow()
	})
}

type inbound struct {
	message protocol.ResolvedMessage
	seq     uint64
	payload []byte
}

// Session is one logical client connection. It outlives transport
// disconnects for the channel's resume window. All methods are safe for
// concurrent use.
type Session struct {
	srv      *Server
	channel  *channelState
	id       string
	identity Identity
	encoding Encoding
	ctx      context.Context
	cancel   context.CancelFunc
	inbound  chan inbound
	// pending counts inbound messages queued or being processed.
	pending sync.WaitGroup
	running sync.WaitGroup

	// Values holds application state (player, room...). It is never sent.
	Values sync.Map

	mu          sync.Mutex
	resumeToken string
	nextSeq     uint64
	outbox      []*outFrame
	outboxBytes int
	peerAck     uint64
	received    uint64
	ackSent     uint64
	conn        *connection
	ended       bool
	// draining drops new requests and notifies unacknowledged (the client
	// resends them to the next server); frozen refuses new output after a
	// hand-off snapshot. restored marks a session taken over from a store.
	draining  bool
	frozen    bool
	restored  bool
	expiresAt time.Time
	endErr    *Error
	expiry    *time.Timer
	ackTimer  *time.Timer
	onEnd     []func(*Error)
}

func (s *Session) ID() string               { return s.id }
func (s *Session) Channel() string          { return s.channel.ID }
func (s *Session) Identity() Identity       { return s.identity }
func (s *Session) Encoding() Encoding       { return s.encoding }
func (s *Session) Context() context.Context { return s.ctx }

// Restored reports whether this server took the session over from another
// instance through Server.Store (OnSessionStart runs for it as well).
func (s *Session) Restored() bool { return s.restored }

// Connected reports whether a transport is currently attached.
func (s *Session) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// OnEnd registers a callback run once when the session ends; it runs
// immediately if the session has already ended.
func (s *Session) OnEnd(callback func(*Error)) {
	s.mu.Lock()
	if !s.ended {
		s.onEnd = append(s.onEnd, callback)
		s.mu.Unlock()
		return
	}
	err := s.endErr
	s.mu.Unlock()
	callback(err)
}

// Close ends the session and tells the client why (for example kicked).
// The client does not reconnect after a non-retryable close.
func (s *Session) Close(code, message string) {
	s.end(&Error{Code: code, Message: message})
}

// Push sends a push message. Reliable pushes are queued while the client is
// briefly disconnected and delivered after it resumes; volatile pushes return
// ErrDropped instead.
func (s *Session) Push(name string, message proto.Message) error {
	m, ok := s.channel.Message(name)
	if !ok || m.Kind != protocol.KindPush {
		return fmt.Errorf("%s: no push message %q", s.channel.ID, name)
	}
	payload, err := s.srv.codec.encode(s.encoding, m.Input, message)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", s.channel.ID, name, err)
	}
	return s.pushEncoded(m, payload)
}

func (s *Session) pushEncoded(m protocol.ResolvedMessage, payload []byte) error {
	if m.Volatile {
		return s.sendVolatile(KindPush, m.ID, payload)
	}
	return s.sendReliable(&outFrame{kind: KindPush, message: m.ID, payload: payload})
}

func (s *Session) maxFrame() int {
	if s.srv.MaxFrameBytes > 0 {
		return s.srv.MaxFrameBytes
	}
	return 1 << 20
}

func (s *Session) sendReliable(f *outFrame) error {
	f.size = len(f.payload) + frameOverhead
	if f.err != nil {
		f.size += len(f.err.Code) + len(f.err.Message)
	}
	if f.size > s.maxFrame() {
		return fmt.Errorf("frame of %d bytes exceeds MaxFrameBytes", f.size)
	}
	maxFrames, maxBytes := s.srv.MaxUnackedFrames, s.srv.MaxUnackedBytes
	if maxFrames <= 0 {
		maxFrames = 4096
	}
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	s.mu.Lock()
	if s.ended || s.frozen {
		s.mu.Unlock()
		return ErrSessionClosed
	}
	if len(s.outbox) >= maxFrames || s.outboxBytes+f.size > maxBytes {
		s.mu.Unlock()
		s.end(&Error{Code: CodeOverloaded, Message: "client is not acknowledging messages", Retryable: true})
		return ErrSessionClosed
	}
	s.nextSeq++
	f.seq = s.nextSeq
	s.outbox = append(s.outbox, f)
	s.outboxBytes += f.size
	if c := s.conn; c != nil {
		c.queue = append(c.queue, f)
		c.signal()
	}
	s.mu.Unlock()
	return nil
}

func (s *Session) sendVolatile(kind Kind, message uint32, payload []byte) error {
	if len(payload)+frameOverhead > s.maxFrame() {
		return fmt.Errorf("frame of %d bytes exceeds MaxFrameBytes", len(payload)+frameOverhead)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return ErrSessionClosed
	}
	c := s.conn
	if c == nil || s.frozen || c.volatile >= maxVolatileQueued {
		return ErrDropped
	}
	c.volatile++
	c.queue = append(c.queue, &outFrame{kind: kind, message: message, payload: payload})
	c.signal()
	return nil
}

// applyAckLocked releases frames the peer has received.
func (s *Session) applyAckLocked(ack uint64) *Error {
	if ack > s.nextSeq {
		return &Error{Code: CodeProtocolError, Message: "acknowledged a frame that was never sent"}
	}
	if ack <= s.peerAck {
		return nil
	}
	s.peerAck = ack
	n := 0
	for n < len(s.outbox) && s.outbox[n].seq <= ack {
		s.outboxBytes -= s.outbox[n].size
		s.outbox[n] = nil
		n++
	}
	s.outbox = s.outbox[n:]
	if len(s.outbox) == 0 {
		s.outbox = nil
	}
	return nil
}

func (s *Session) heartbeat() time.Duration {
	return time.Duration(s.channel.Heartbeat) * time.Millisecond
}

// attach makes c the session's transport and queues welcome plus every
// unacknowledged frame. A previous transport is closed (the client replaced it).
func (s *Session) attach(c *connection, peerAck uint64, resumed bool) error {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return &Error{Code: CodeSessionExpired, Message: "session ended; start a new session", Retryable: true}
	}
	if err := s.applyAckLocked(peerAck); err != nil {
		s.mu.Unlock()
		s.end(err)
		return err
	}
	old := s.conn
	if s.expiry != nil {
		s.expiry.Stop()
		s.expiry = nil
	}
	s.expiresAt = time.Time{}
	welcome := &Welcome{SessionID: s.id, ResumeToken: s.resumeToken, Resumed: resumed, HeartbeatMS: uint32(s.channel.Heartbeat), ResumeWindowMS: uint32(s.channel.Resume)}
	c.queue = append(make([]*outFrame, 0, len(s.outbox)+1), &outFrame{kind: KindWelcome, welcome: welcome})
	c.queue = append(c.queue, s.outbox...)
	s.conn = c
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	c.signal()
	return nil
}

// detach handles a broken transport. The session waits for a resume.
func (s *Session) detach(c *connection) {
	c.close()
	s.mu.Lock()
	if s.conn != c || s.ended {
		s.mu.Unlock()
		return
	}
	s.conn = nil
	window := time.Duration(s.channel.Resume) * time.Millisecond
	if window > 0 {
		s.expiresAt = time.Now().Add(window)
		s.expiry = time.AfterFunc(window, func() {
			s.endIf(&Error{Code: CodeSessionExpired, Message: "resume window elapsed", Retryable: true}, func() bool { return s.conn == nil })
		})
	}
	s.mu.Unlock()
	s.srv.observe(Event{Type: "disconnect", Channel: s.channel.ID, SessionID: s.id, UserID: s.identity.UserID})
	if window == 0 {
		s.end(&Error{Code: CodeSessionExpired, Message: "channel does not resume sessions", Retryable: true})
	}
}

func (s *Session) end(e *Error) { s.endIf(e, nil) }

// endIf ends the session when condition (checked under the lock) holds.
func (s *Session) endIf(e *Error, condition func() bool) { s.endWithIf(e, e, condition) }

// endWith reports e to the application and sends client to the client.
func (s *Session) endWith(e, client *Error) { s.endWithIf(e, client, nil) }

func (s *Session) endWithIf(e, client *Error, condition func() bool) {
	s.mu.Lock()
	if s.ended || (condition != nil && !condition()) {
		s.mu.Unlock()
		return
	}
	s.ended, s.endErr = true, e
	c := s.conn
	s.conn = nil
	if s.expiry != nil {
		s.expiry.Stop()
	}
	if s.ackTimer != nil {
		s.ackTimer.Stop()
	}
	s.outbox, s.outboxBytes = nil, 0
	callbacks := s.onEnd
	s.onEnd = nil
	s.mu.Unlock()
	s.cancel()
	s.srv.remove(s, e)
	if c != nil {
		go c.closeWith(s.encoding, client, time.Second)
	}
	for _, callback := range callbacks {
		callback(e)
	}
	if s.srv.OnSessionEnd != nil {
		s.srv.OnSessionEnd(s, e)
	}
	s.srv.observe(Event{Type: "session_end", Channel: s.channel.ID, SessionID: s.id, UserID: s.identity.UserID, Code: e.Code, Reason: e.Message})
}

func (s *Session) writeLoop(c *connection) {
	typ := messageType(s.encoding)
	timeout := s.srv.writeTimeout()
	for {
		select {
		case <-c.wake:
		case <-c.done:
			return
		}
		for {
			s.mu.Lock()
			if s.conn != c {
				s.mu.Unlock()
				return
			}
			var f *outFrame
			if len(c.queue) > 0 {
				f = c.queue[0]
				c.queue[0] = nil
				c.queue = c.queue[1:]
				if f.seq == 0 && (f.kind == KindPush || f.kind == KindNotify) {
					c.volatile--
				}
				if f.seq != 0 && f.seq <= s.peerAck {
					s.mu.Unlock()
					continue
				}
			} else if s.received > s.ackSent {
				f = &outFrame{kind: KindAck}
			} else {
				s.mu.Unlock()
				break
			}
			frame := Frame{Kind: f.kind, Seq: f.seq, Ack: s.received, Message: f.message, ReplyTo: f.replyTo, Payload: f.payload, Error: f.err, Welcome: f.welcome}
			s.ackSent = s.received
			s.mu.Unlock()
			data, err := frame.Encode(s.encoding)
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				err = c.ws.Write(ctx, typ, data)
				cancel()
			}
			if err != nil {
				s.detach(c)
				return
			}
		}
	}
}

func (s *Session) readLoop(c *connection) {
	idle := 3*s.heartbeat() + time.Second
	typ := messageType(s.encoding)
	limit := float64(s.srv.MaxFramesPerSecond)
	c.tokens, c.last = 2*limit, time.Now()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), idle)
		got, data, err := c.ws.Read(ctx)
		cancel()
		if err != nil {
			s.detach(c)
			return
		}
		if len(data) == 0 {
			continue // Empty messages carry nothing (some WebSocket stacks send them); never a frame.
		}
		if limit > 0 {
			now := time.Now()
			c.tokens += now.Sub(c.last).Seconds() * limit
			if c.tokens > 2*limit {
				c.tokens = 2 * limit
			}
			c.last = now
			if c.tokens < 1 {
				s.end(&Error{Code: CodeRateLimited, Message: "too many frames"})
				return
			}
			c.tokens--
		}
		var f *Frame
		if got == typ {
			f, err = DecodeFrame(s.encoding, data)
		} else {
			err = errMalformed
		}
		if err != nil {
			s.end(&Error{Code: CodeProtocolError, Message: err.Error()})
			return
		}
		if stop, e := s.receive(c, f); e != nil {
			s.end(e)
			return
		} else if stop {
			return
		}
	}
}

// receive applies one frame from the client. stop is true when the
// connection is no longer the session's transport.
func (s *Session) receive(c *connection, f *Frame) (stop bool, _ *Error) {
	if f.Kind == KindClose {
		s.endIf(&Error{Code: CodeClosed, Message: "closed by client"}, func() bool { return s.conn == c })
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != c {
		return true, nil
	}
	if e := s.applyAckLocked(f.Ack); e != nil {
		return false, e
	}
	switch f.Kind {
	case KindPing:
		c.queue = append(c.queue, &outFrame{kind: KindPong})
		c.signal()
	case KindPong, KindAck:
	case KindRequest, KindNotify:
		m, ok := s.channel.MessageByID(f.Message)
		if !ok || (f.Kind == KindRequest) != (m.Kind == protocol.KindRequest) || (f.Kind == KindNotify) != (m.Kind == protocol.KindNotify) {
			return false, &Error{Code: CodeProtocolError, Message: fmt.Sprintf("message %d is not a %s", f.Message, f.Kind)}
		}
		if (f.Seq == 0) != m.Volatile {
			return false, &Error{Code: CodeProtocolError, Message: fmt.Sprintf("message %s must be %s", m.Name, map[bool]string{true: "volatile", false: "sequenced"}[m.Volatile])}
		}
		if s.draining {
			// Handing off: neither run nor acknowledge it. A sequenced frame
			// is retransmitted to the next server after the client resumes.
			return false, nil
		}
		in := inbound{message: m, seq: f.Seq, payload: f.Payload}
		if m.Volatile {
			s.pending.Add(1)
			select {
			case s.inbound <- in:
			default: // Volatile input is dropped under load.
				s.pending.Done()
			}
			return false, nil
		}
		if f.Seq <= s.received {
			s.armAckLocked() // A retransmission: confirm again so the client releases it.
			return false, nil
		}
		if f.Seq != s.received+1 {
			return false, &Error{Code: CodeProtocolError, Message: "sequence gap"}
		}
		s.pending.Add(1)
		select {
		case s.inbound <- in:
		default:
			s.pending.Done()
			return false, &Error{Code: CodeOverloaded, Message: "too many queued messages", Retryable: true}
		}
		s.received = f.Seq
		s.armAckLocked()
	default:
		return false, &Error{Code: CodeProtocolError, Message: "unexpected " + f.Kind.String() + " frame from client"}
	}
	return false, nil
}

func (s *Session) armAckLocked() {
	if s.ackTimer != nil {
		return
	}
	delay := s.srv.AckDelay
	if delay <= 0 {
		delay = 50 * time.Millisecond
	}
	if s.received == s.ackSent {
		s.ackSent-- // Force one ACK for a duplicate even if nothing new arrived.
	}
	s.ackTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		s.ackTimer = nil
		if s.conn != nil && s.received > s.ackSent {
			s.conn.signal()
		}
		s.mu.Unlock()
	})
}

func (s *Session) dispatch() {
	defer s.srv.wg.Done()
	defer s.running.Wait()
	concurrency := s.srv.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	slots := make(chan struct{}, concurrency)
	for {
		select {
		case <-s.ctx.Done():
			s.discardInbound()
			return
		case in := <-s.inbound:
			if concurrency == 1 {
				s.process(in)
				continue
			}
			select {
			case slots <- struct{}{}:
			case <-s.ctx.Done():
				s.pending.Done() // in is dropped with the session.
				s.discardInbound()
				return
			}
			s.running.Add(1)
			go func() {
				defer s.running.Done()
				defer func() { <-slots }()
				s.process(in)
			}()
		}
	}
}

// discardInbound releases messages left in the queue of an ended session.
// Nothing is enqueued after the session ends, so this empties it for good.
func (s *Session) discardInbound() {
	for {
		select {
		case <-s.inbound:
			s.pending.Done()
		default:
			return
		}
	}
}

// startDrain stops taking new requests and notifies (see receive). It
// reports false if the session has already ended.
func (s *Session) startDrain() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	s.draining = true
	return true
}

// waitDrained waits until every queued message has been processed, so
// their responses are in the outbox. It reports false if the session ended
// or ctx expired first.
func (s *Session) waitDrained(ctx context.Context) bool {
	done := make(chan struct{})
	go func() {
		s.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return s.ctx.Err() == nil
	case <-s.ctx.Done():
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Session) process(in inbound) {
	defer s.pending.Done()
	started := time.Now()
	event := Event{Type: in.message.Kind, Channel: s.channel.ID, SessionID: s.id, UserID: s.identity.UserID, Message: in.message.Name}
	defer func() {
		event.Duration = time.Since(started)
		s.srv.observe(event)
	}()
	s.srv.mu.RLock()
	request, notify := s.channel.requests[in.message.Name], s.channel.notifies[in.message.Name]
	s.srv.mu.RUnlock()
	if in.message.Kind == protocol.KindNotify {
		if notify == nil {
			event.Code = CodeNotImplemented
			return
		}
		message := notify.newIn()
		if err := s.srv.codec.decode(s.encoding, in.payload, message); err != nil {
			event.Code = CodeBadRequest
			return
		}
		func() {
			defer func() {
				if recover() != nil {
					event.Code = CodeInternal
				}
			}()
			notify.handler(s.ctx, s, message)
		}()
		return
	}
	respond := func(payload []byte, e *Error) {
		if e != nil {
			event.Code = e.Code
		}
		if payload == nil && e == nil && s.encoding == EncodingJSON {
			payload = []byte("{}")
		}
		s.sendReliable(&outFrame{kind: KindResponse, message: in.message.ID, replyTo: in.seq, payload: payload, err: e})
	}
	if request == nil {
		respond(nil, &Error{Code: CodeNotImplemented, Message: "no handler for " + in.message.Name})
		return
	}
	message := request.newIn()
	if err := s.srv.codec.decode(s.encoding, in.payload, message); err != nil {
		respond(nil, &Error{Code: CodeBadRequest, Message: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(in.message.TimeoutMS)*time.Millisecond)
	defer cancel()
	response, err := invoke(ctx, request.handler, s, message)
	if s.ctx.Err() != nil {
		return // Session ended; nobody can receive the answer.
	}
	if err != nil {
		respond(nil, toError(err))
		return
	}
	payload, err := s.srv.codec.encode(s.encoding, in.message.Output, response)
	if err != nil {
		respond(nil, &Error{Code: CodeInternal, Message: "handler returned an invalid response"})
		return
	}
	if len(payload)+frameOverhead > s.maxFrame() {
		respond(nil, &Error{Code: CodeInternal, Message: "response exceeds MaxFrameBytes"})
		return
	}
	respond(payload, nil)
}

func invoke(ctx context.Context, handler RequestHandler, s *Session, request proto.Message) (response proto.Message, err error) {
	defer func() {
		if recover() != nil {
			response, err = nil, &Error{Code: CodeInternal, Message: "request failed"}
		}
	}()
	return handler(ctx, s, request)
}

func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeTimeout, Message: "request deadline exceeded"}
	}
	return &Error{Code: CodeInternal, Message: "request failed"}
}
