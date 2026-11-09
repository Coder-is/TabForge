package realtime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// Identity is what Authenticate returns for a bearer token. UserID groups the
// sessions of one player (see SingleSessionPerUser); Data is application-owned.
type Identity struct {
	UserID string
	Data   any
}

type RequestHandler func(ctx context.Context, s *Session, request proto.Message) (proto.Message, error)
type NotifyHandler func(ctx context.Context, s *Session, message proto.Message)

// Event reports session lifecycle and message results to logs/metrics. It
// never contains tokens or payloads. Observe must not block.
type Event struct {
	Type      string // session_start, session_resume, disconnect, session_end, request, notify
	Channel   string
	SessionID string
	UserID    string
	Message   string // request/notify name
	Code      string // error code, empty on success
	Duration  time.Duration
}

// Server accepts WebSocket connections for every channel of a contract.
// Configure fields and register handlers before serving.
type Server struct {
	Contract *protocol.Contract
	// Authenticate validates the bearer token of a new session on channels
	// with auth "bearer". Without it, bearer channels reject every session.
	Authenticate func(ctx context.Context, channel, token string) (Identity, error)
	// OriginPatterns lists allowed browser origins (host patterns, see
	// coder/websocket). Same-host requests and non-browser clients always pass.
	OriginPatterns []string
	// RequireSchemaHash rejects hellos without the exact contract schema hash.
	RequireSchemaHash bool
	// SingleSessionPerUser closes older sessions of the same UserID with
	// duplicate_login when a new session starts.
	SingleSessionPerUser bool
	// MaxFrameBytes bounds a single WebSocket message in either direction.
	MaxFrameBytes int
	// MaxUnackedFrames and MaxUnackedBytes bound what a session retains for
	// retransmission. A client that stops acknowledging (or stays offline
	// while pushes accumulate) is closed with overloaded.
	MaxUnackedFrames int
	MaxUnackedBytes  int
	// MaxQueuedMessages bounds received requests/notifies waiting for handlers.
	MaxQueuedMessages int
	// MaxFramesPerSecond limits frames read from one connection; 0 disables.
	MaxFramesPerSecond int
	// Concurrency is how many requests of one session run at once. The default
	// 1 runs a session's requests and notifies strictly in arrival order.
	Concurrency      int
	HandshakeTimeout time.Duration
	WriteTimeout     time.Duration
	// AckDelay coalesces acknowledgements when there is nothing else to send.
	AckDelay time.Duration
	// Called after a session starts (before its first message is read) and
	// after it ends. Both run synchronously; keep them short.
	OnSessionStart func(*Session)
	OnSessionEnd   func(*Session, *Error)
	Observe        func(Event)

	codec      *payloadCodec
	schemaHash string
	mu         sync.RWMutex
	channels   map[string]*channelState
	sessions   map[string]*Session
	// ended remembers why sessions were closed for good, so a client whose
	// close frame was lost learns the reason when it tries to resume.
	ended   map[string]tombstone
	closing bool
	wg      sync.WaitGroup
}

type channelState struct {
	protocol.ResolvedChannel
	requests map[string]*requestRoute
	notifies map[string]*notifyRoute
}

type requestRoute struct {
	message protocol.ResolvedMessage
	newIn   func() proto.Message
	handler RequestHandler
}

type notifyRoute struct {
	message protocol.ResolvedMessage
	newIn   func() proto.Message
	handler NotifyHandler
}

func NewServer(c *protocol.Contract) *Server {
	s := &Server{
		Contract:          c,
		RequireSchemaHash: true,
		MaxFrameBytes:     1 << 20,
		MaxUnackedFrames:  4096,
		MaxUnackedBytes:   8 << 20,
		MaxQueuedMessages: 1024,
		Concurrency:       1,
		HandshakeTimeout:  10 * time.Second,
		WriteTimeout:      10 * time.Second,
		AckDelay:          50 * time.Millisecond,
		codec:             newPayloadCodec(c),
		schemaHash:        c.Fingerprint(),
		channels:          map[string]*channelState{},
		sessions:          map[string]*Session{},
		ended:             map[string]tombstone{},
	}
	for _, ch := range c.Channels {
		s.channels[ch.ID] = &channelState{ResolvedChannel: ch, requests: map[string]*requestRoute{}, notifies: map[string]*notifyRoute{}}
	}
	return s
}

func (s *Server) lookup(channel, name, kind string) (*channelState, protocol.ResolvedMessage, error) {
	ch := s.channels[channel]
	if ch == nil {
		return nil, protocol.ResolvedMessage{}, fmt.Errorf("unknown channel %q", channel)
	}
	m, ok := ch.Message(name)
	if !ok || m.Kind != kind {
		return nil, protocol.ResolvedMessage{}, fmt.Errorf("%s: no %s message %q", channel, kind, name)
	}
	return ch, m, nil
}

// HandleRequest registers an untyped handler; requests are dynamic messages.
func (s *Server) HandleRequest(channel, name string, handler RequestHandler) error {
	return handleRequest[proto.Message](s, channel, name, handler)
}

// HandleNotify registers an untyped notify handler.
func (s *Server) HandleNotify(channel, name string, handler NotifyHandler) error {
	return handleNotify[proto.Message](s, channel, name, handler)
}

// OnRequest registers a typed handler. Req and Resp are generated Go message
// types whose schema must equal the contract's (checked at registration).
func OnRequest[Req, Resp proto.Message](s *Server, channel, name string, handler func(context.Context, *Session, Req) (Resp, error)) error {
	if handler == nil {
		return fmt.Errorf("nil handler for %s.%s", channel, name)
	}
	_, m, err := s.lookup(channel, name, protocol.KindRequest)
	if err != nil {
		return err
	}
	var zero Resp
	if any(zero) != nil {
		if err := s.codec.check(zero.ProtoReflect().Descriptor(), m.Output); err != nil {
			return fmt.Errorf("%s.%s response: %w", channel, name, err)
		}
	}
	return handleRequest[Req](s, channel, name, func(ctx context.Context, session *Session, request proto.Message) (proto.Message, error) {
		response, err := handler(ctx, session, request.(Req))
		if err != nil {
			return nil, err
		}
		return response, nil
	})
}

// OnNotify registers a typed notify handler.
func OnNotify[T proto.Message](s *Server, channel, name string, handler func(context.Context, *Session, T)) error {
	if handler == nil {
		return fmt.Errorf("nil handler for %s.%s", channel, name)
	}
	return handleNotify[T](s, channel, name, func(ctx context.Context, session *Session, message proto.Message) {
		handler(ctx, session, message.(T))
	})
}

func handleRequest[Req proto.Message](s *Server, channel, name string, handler RequestHandler) error {
	ch, m, err := s.lookup(channel, name, protocol.KindRequest)
	if err != nil {
		return err
	}
	newIn, err := factory[Req](s.codec, m.Input)
	if err != nil {
		return fmt.Errorf("%s.%s request: %w", channel, name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if handler == nil || ch.requests[name] != nil {
		return fmt.Errorf("nil or duplicate handler for %s.%s", channel, name)
	}
	ch.requests[name] = &requestRoute{message: m, newIn: newIn, handler: handler}
	return nil
}

func handleNotify[T proto.Message](s *Server, channel, name string, handler NotifyHandler) error {
	ch, m, err := s.lookup(channel, name, protocol.KindNotify)
	if err != nil {
		return err
	}
	newIn, err := factory[T](s.codec, m.Input)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", channel, name, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if handler == nil || ch.notifies[name] != nil {
		return fmt.Errorf("nil or duplicate handler for %s.%s", channel, name)
	}
	ch.notifies[name] = &notifyRoute{message: m, newIn: newIn, handler: handler}
	return nil
}

func (s *Server) observe(e Event) {
	if s.Observe != nil {
		s.Observe(e)
	}
}

// Session returns a live session by id.
func (s *Server) Session(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	session, ok := s.sessions[id]
	return session, ok
}

// Sessions returns live sessions, optionally only those of userID.
func (s *Server) Sessions(userID string) []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Session
	for _, session := range s.sessions {
		if userID == "" || session.identity.UserID == userID {
			out = append(out, session)
		}
	}
	return out
}

// Shutdown stops accepting sessions, closes every session with the retryable
// server_shutdown code and waits for running handlers until ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		session.end(&Error{Code: CodeServerShutdown, Message: "server is shutting down", Retryable: true})
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func randomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ServeHTTP upgrades requests on channel paths. Mount the server on an
// http.Server without WriteTimeout (connections are long-lived).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var ch *channelState
	for _, candidate := range s.channels {
		if candidate.Path == r.URL.Path {
			ch = candidate
		}
	}
	if ch == nil {
		http.NotFound(w, r)
		return
	}
	s.mu.RLock()
	closing := s.closing
	s.mu.RUnlock()
	if closing {
		http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{SubprotocolProto, SubprotocolJSON},
		OriginPatterns:  s.OriginPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return // Accept already wrote the HTTP error.
	}
	enc, ok := encodingFor(ws.Subprotocol())
	if !ok {
		ws.Close(websocket.StatusProtocolError, "offer "+SubprotocolProto+" or "+SubprotocolJSON)
		return
	}
	limit := s.MaxFrameBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	ws.SetReadLimit(int64(limit))
	s.wg.Add(1)
	defer s.wg.Done()
	s.serveConn(r.Context(), ch, ws, enc)
}

func (s *Server) handshakeTimeout() time.Duration {
	if s.HandshakeTimeout > 0 {
		return s.HandshakeTimeout
	}
	return 10 * time.Second
}

func (s *Server) writeTimeout() time.Duration {
	if s.WriteTimeout > 0 {
		return s.WriteTimeout
	}
	return 10 * time.Second
}

// reject sends a close frame on a connection that has no session yet.
func (s *Server) reject(ws *websocket.Conn, enc Encoding, e *Error) {
	data, _ := (&Frame{Kind: KindClose, Error: e}).Encode(enc)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ws.Write(ctx, messageType(enc), data)
	ws.Close(websocket.StatusNormalClosure, e.Code)
}

func messageType(enc Encoding) websocket.MessageType {
	if enc == EncodingJSON {
		return websocket.MessageText
	}
	return websocket.MessageBinary
}

func (s *Server) serveConn(ctx context.Context, ch *channelState, ws *websocket.Conn, enc Encoding) {
	readCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	typ, data, err := ws.Read(readCtx)
	cancel()
	if err != nil {
		ws.CloseNow()
		return
	}
	var hello *Frame
	if typ == messageType(enc) {
		hello, err = DecodeFrame(enc, data)
	}
	if hello == nil || err != nil || hello.Kind != KindHello {
		s.reject(ws, enc, &Error{Code: CodeProtocolError, Message: "the first frame must be hello"})
		return
	}
	h := hello.Hello
	if h.ProtocolVersion != s.Contract.Manifest.Version {
		s.reject(ws, enc, &Error{Code: CodeVersionMismatch, Message: "protocol version " + s.Contract.Manifest.Version + " is required"})
		return
	}
	if (h.SchemaHash != "" || s.RequireSchemaHash) && h.SchemaHash != s.codecHash() {
		s.reject(ws, enc, &Error{Code: CodeSchemaMismatch, Message: "schema hash does not match the server contract"})
		return
	}
	var session *Session
	resumed := false
	if h.SessionID != "" {
		s.mu.RLock()
		candidate := s.sessions[h.SessionID]
		ended, hasEnded := s.ended[h.SessionID]
		s.mu.RUnlock()
		if candidate == nil && hasEnded && time.Now().Before(ended.expires) && subtle.ConstantTimeCompare([]byte(ended.resumeToken), []byte(h.ResumeToken)) == 1 {
			// The close frame was lost with the connection: repeat the real reason.
			s.reject(ws, enc, ended.err)
			return
		}
		if candidate == nil || candidate.channel != ch || candidate.encoding != enc || subtle.ConstantTimeCompare([]byte(candidate.resumeToken), []byte(h.ResumeToken)) != 1 {
			s.reject(ws, enc, &Error{Code: CodeSessionExpired, Message: "session cannot be resumed; start a new session", Retryable: true})
			return
		}
		session, resumed = candidate, true
	} else {
		identity := Identity{}
		if ch.Auth == "bearer" {
			if s.Authenticate == nil || h.Token == "" {
				s.reject(ws, enc, &Error{Code: CodeUnauthorized, Message: "authorization required"})
				return
			}
			authCtx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
			identity, err = s.Authenticate(authCtx, ch.ID, h.Token)
			cancel()
			if err != nil {
				code, message := CodeUnauthorized, "authorization failed"
				var e *Error
				if errors.As(err, &e) {
					code, message = e.Code, e.Message
				}
				s.reject(ws, enc, &Error{Code: code, Message: message})
				return
			}
		}
		session, err = s.newSession(ch, enc, identity)
		if err != nil {
			s.reject(ws, enc, err.(*Error))
			return
		}
	}
	c := newConnection(ws)
	if err := session.attach(c, hello.Ack, resumed); err != nil {
		s.reject(ws, enc, err.(*Error))
		return
	}
	if resumed {
		s.observe(Event{Type: "session_resume", Channel: ch.ID, SessionID: session.id, UserID: session.identity.UserID})
	} else {
		s.observe(Event{Type: "session_start", Channel: ch.ID, SessionID: session.id, UserID: session.identity.UserID})
		if s.OnSessionStart != nil {
			s.OnSessionStart(session)
		}
	}
	go session.writeLoop(c)
	session.readLoop(c)
}

// codecHash is the schema hash clients must present.
func (s *Server) codecHash() string {
	return s.schemaHash
}

func (s *Server) newSession(ch *channelState, enc Encoding, identity Identity) (*Session, error) {
	id, err := randomID(16)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "could not create session"}
	}
	token, err := randomToken()
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "could not create session"}
	}
	queue := s.MaxQueuedMessages
	if queue <= 0 {
		queue = 1024
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &Session{srv: s, channel: ch, id: id, resumeToken: token, identity: identity, encoding: enc, ctx: ctx, cancel: cancel, inbound: make(chan inbound, queue)}
	var replaced []*Session
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		cancel()
		return nil, &Error{Code: CodeServerShutdown, Message: "server is shutting down", Retryable: true}
	}
	if s.SingleSessionPerUser && identity.UserID != "" {
		for _, other := range s.sessions {
			if other.identity.UserID == identity.UserID && other.channel == ch {
				replaced = append(replaced, other)
			}
		}
	}
	s.sessions[id] = session
	s.wg.Add(1)
	s.mu.Unlock()
	for _, other := range replaced {
		other.end(&Error{Code: CodeDuplicateLogin, Message: "signed in from another connection"})
	}
	go session.dispatch()
	return session, nil
}

type tombstone struct {
	resumeToken string
	err         *Error
	expires     time.Time
}

// maxTombstones bounds memory if many sessions are kicked at once.
const maxTombstones = 100000

func (s *Server) remove(session *Session, reason *Error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[session.id] == session {
		delete(s.sessions, session.id)
	}
	if reason.Retryable || session.channel.Resume == 0 {
		return // Retryable ends (expiry, shutdown) are reported as session_expired.
	}
	if len(s.ended) >= maxTombstones {
		for id, t := range s.ended {
			if now.After(t.expires) {
				delete(s.ended, id)
			}
		}
	}
	if len(s.ended) < maxTombstones {
		s.ended[session.id] = tombstone{resumeToken: session.resumeToken, err: reason, expires: now.Add(time.Duration(session.channel.Resume) * time.Millisecond)}
	}
}
