package realtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// SessionStore carries resumable sessions between server instances, so a
// rolling deploy does not cost clients their sessions.
//
// During Shutdown, a server with a Store stops taking new input, lets
// running handlers finish, and writes each session (sequence numbers,
// unacknowledged frames, application state from ExportSession) under a key
// derived from the session id and resume token. Only then does it tell the
// client to reconnect. The instance that receives the resume takes the
// snapshot and continues the session; the client sees "resumed" and nothing
// is lost or executed twice.
//
// Take must be atomic (get and delete in one step, such as Redis GETDEL) so
// only one instance can ever continue a session. Keys contain a hash of the
// resume token: without the token a snapshot can be neither read nor
// deleted.
type SessionStore interface {
	// Put stores value under key for at most ttl.
	Put(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Take returns and deletes the value; (nil, nil) if there is none.
	Take(ctx context.Context, key string) ([]byte, error)
}

// CodeHandedOff is the end reason a server reports to OnSessionEnd and
// Session.OnEnd when a session moved to another instance. The client itself
// receives server_shutdown and resumes there.
const CodeHandedOff = "handed_off"

// MemoryStore is a SessionStore inside one process: for tests, and for
// replacing one Server with another in the same process.
type MemoryStore struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	value   []byte
	expires time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: map[string]memoryEntry{}}
}

func (m *MemoryStore) Put(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, e := range m.entries {
		if now.After(e.expires) {
			delete(m.entries, k)
		}
	}
	m.entries[key] = memoryEntry{value: append([]byte(nil), value...), expires: now.Add(ttl)}
	return nil
}

func (m *MemoryStore) Take(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	delete(m.entries, key)
	if !ok || time.Now().After(e.expires) {
		return nil, nil
	}
	return e.value, nil
}

// Len reports how many unexpired entries the store holds.
func (m *MemoryStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		if time.Now().Before(e.expires) {
			n++
		}
	}
	return n
}

const (
	sessionKeyPrefix = "tabforge-realtime:session:"
	endedKeyPrefix   = "tabforge-realtime:ended:"
	movingKeyPrefix  = "tabforge-realtime:moving:"
	snapshotVersion  = 1
	// maxHandOffWait bounds how long clients are asked to retry while
	// their session is being handed off (less if Shutdown's ctx is shorter).
	maxHandOffWait = 30 * time.Second
)

// storeKey identifies a session by its id and resume token without
// revealing the token.
func storeKey(prefix, sessionID, resumeToken string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + resumeToken))
	return prefix + hex.EncodeToString(sum[:])
}

type snapshot struct {
	Version    int             `json:"v"`
	Channel    string          `json:"channel"`
	Encoding   string          `json:"encoding"`
	SchemaHash string          `json:"schemaHash"`
	UserID     string          `json:"userId,omitempty"`
	NextSeq    uint64          `json:"nextSeq"`
	PeerAck    uint64          `json:"peerAck"`
	Received   uint64          `json:"received"`
	Outbox     []snapshotFrame `json:"outbox,omitempty"`
	App        []byte          `json:"app,omitempty"`
}

type snapshotFrame struct {
	Kind    Kind   `json:"kind"`
	Seq     uint64 `json:"seq"`
	Message uint32 `json:"message"`
	ReplyTo uint64 `json:"replyTo,omitempty"`
	Payload []byte `json:"payload,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

func decodeSnapshot(data []byte) (*snapshot, error) {
	snap := &snapshot{}
	if err := json.Unmarshal(data, snap); err != nil {
		return nil, err
	}
	if snap.Version != snapshotVersion {
		return nil, fmt.Errorf("snapshot version %d is not supported", snap.Version)
	}
	if snap.PeerAck > snap.NextSeq {
		return nil, errors.New("snapshot acknowledges frames it never sent")
	}
	last := snap.PeerAck
	for _, f := range snap.Outbox {
		if f.Seq <= last || f.Seq > snap.NextSeq || (f.Kind != KindResponse && f.Kind != KindPush) {
			return nil, errors.New("snapshot outbox is out of order")
		}
		last = f.Seq
	}
	return snap, nil
}

// freeze stops a drained session from accepting more output and captures
// it. ok is false if the session has already ended.
func (s *Session) freeze() (snap *snapshot, ttl time.Duration, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, 0, false
	}
	s.frozen = true
	ttl = time.Duration(s.channel.Resume) * time.Millisecond
	if s.conn == nil && !s.expiresAt.IsZero() {
		ttl = time.Until(s.expiresAt)
	}
	snap = &snapshot{
		Version: snapshotVersion, Channel: s.channel.ID, Encoding: s.encoding.Subprotocol(), SchemaHash: s.srv.schemaHash,
		UserID: s.identity.UserID, NextSeq: s.nextSeq, PeerAck: s.peerAck, Received: s.received,
	}
	for _, f := range s.outbox {
		snap.Outbox = append(snap.Outbox, snapshotFrame{Kind: f.kind, Seq: f.seq, Message: f.message, ReplyTo: f.replyTo, Payload: f.payload, Error: f.err})
	}
	return snap, ttl, true
}

// handOff drains every resumable session, stores it, and only then tells
// its client to reconnect. It returns the sessions it could not hand off.
func (s *Server) handOff(ctx context.Context, sessions []*Session, ended map[string]tombstone) []*Session {
	// A client whose connection drops during the drain may reach the next
	// server before its snapshot is stored. A marker with a fixed deadline
	// tells that server to have the client retry instead of starting over.
	deadline := time.Now().Add(maxHandOffWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	marker := []byte(strconv.FormatInt(deadline.UnixMilli(), 10))
	var draining, rest []*Session
	for _, session := range sessions {
		if session.channel.Resume > 0 && session.startDrain() {
			s.Store.Put(ctx, storeKey(movingKeyPrefix, session.id, session.resumeToken), marker, time.Until(deadline))
			draining = append(draining, session)
		} else {
			rest = append(rest, session)
		}
	}
	defer func() {
		for _, session := range draining {
			s.Store.Take(ctx, storeKey(movingKeyPrefix, session.id, session.resumeToken))
		}
	}()
	// Drain all sessions before freezing any, so pushes that handlers send
	// to other sessions (rooms, chat) are part of those snapshots.
	var drained []*Session
	for _, session := range draining {
		if session.waitDrained(ctx) {
			drained = append(drained, session)
		} else {
			rest = append(rest, session)
		}
	}
	for _, session := range drained {
		if !s.storeSession(ctx, session) {
			rest = append(rest, session)
		}
	}
	// Kicked sessions whose close frame may have been lost keep their reason.
	now := time.Now()
	for id, t := range ended {
		if ttl := t.expires.Sub(now); ttl > 0 {
			data, _ := json.Marshal(t.err)
			s.Store.Put(ctx, storeKey(endedKeyPrefix, id, t.resumeToken), data, ttl)
		}
	}
	return rest
}

func (s *Server) storeSession(ctx context.Context, session *Session) bool {
	snap, ttl, ok := session.freeze()
	if !ok {
		return true // Ended while draining; nothing to hand off.
	}
	if ttl <= 0 {
		return false
	}
	if s.ExportSession != nil {
		app, err := s.ExportSession(session)
		if err != nil {
			s.observe(Event{Type: "handoff_failed", Channel: session.channel.ID, SessionID: session.id, UserID: session.identity.UserID, Reason: "export: " + err.Error()})
			return false
		}
		snap.App = app
	}
	data, err := json.Marshal(snap)
	if err == nil {
		err = s.Store.Put(ctx, storeKey(sessionKeyPrefix, session.id, session.resumeToken), data, ttl)
	}
	if err != nil {
		s.observe(Event{Type: "handoff_failed", Channel: session.channel.ID, SessionID: session.id, UserID: session.identity.UserID, Reason: err.Error()})
		return false
	}
	s.observe(Event{Type: "session_handoff", Channel: session.channel.ID, SessionID: session.id, UserID: session.identity.UserID})
	session.endWith(&Error{Code: CodeHandedOff, Message: "session moved to another server", Retryable: true},
		&Error{Code: CodeServerShutdown, Message: "server is shutting down", Retryable: true})
	return true
}

// takeFromStore continues a session another instance handed off, or
// reports the reason a session ended there. It returns (nil, nil) when the
// store has neither.
func (s *Server) takeFromStore(ctx context.Context, ch *channelState, enc Encoding, sessionID, resumeToken string) (*Session, *Error) {
	expired := &Error{Code: CodeSessionExpired, Message: "session cannot be resumed; start a new session", Retryable: true}
	ctx, cancel := context.WithTimeout(ctx, s.handshakeTimeout())
	defer cancel()
	data, err := s.Store.Take(ctx, storeKey(sessionKeyPrefix, sessionID, resumeToken))
	if err != nil {
		s.observe(Event{Type: "restore_failed", Channel: ch.ID, SessionID: sessionID, Reason: err.Error()})
		return nil, expired
	}
	if data == nil {
		ended, err := s.Store.Take(ctx, storeKey(endedKeyPrefix, sessionID, resumeToken))
		reason := &Error{}
		if err == nil && ended != nil && json.Unmarshal(ended, reason) == nil && reason.Code != "" {
			return nil, reason
		}
		if s.handOffPending(ctx, sessionID, resumeToken) {
			return nil, &Error{Code: CodeServerShutdown, Message: "session is moving to this server; retry", Retryable: true}
		}
		return nil, nil
	}
	snap, err := decodeSnapshot(data)
	if err == nil && (snap.Channel != ch.ID || snap.Encoding != enc.Subprotocol() || snap.SchemaHash != s.schemaHash) {
		err = errors.New("snapshot is for another channel, encoding or schema")
	}
	if err != nil {
		s.observe(Event{Type: "restore_failed", Channel: ch.ID, SessionID: sessionID, Reason: err.Error()})
		return nil, expired
	}
	session, e := s.restoreSession(ch, enc, sessionID, resumeToken, snap)
	if e != nil {
		return nil, e
	}
	return session, nil
}

// handOffPending reports whether another server is still storing this
// session. Take is the only read the store offers, so the marker is put
// back with its original deadline: it never outlives the hand-off, even if
// the old server dies while draining.
func (s *Server) handOffPending(ctx context.Context, sessionID, resumeToken string) bool {
	key := storeKey(movingKeyPrefix, sessionID, resumeToken)
	marker, err := s.Store.Take(ctx, key)
	if err != nil || marker == nil {
		return false
	}
	ms, err := strconv.ParseInt(string(marker), 10, 64)
	if err != nil {
		return false
	}
	ttl := time.Until(time.UnixMilli(ms))
	if ttl <= 0 {
		return false
	}
	s.Store.Put(ctx, key, marker, ttl)
	return true
}

func (s *Server) restoreSession(ch *channelState, enc Encoding, id, token string, snap *snapshot) (*Session, *Error) {
	identity := Identity{UserID: snap.UserID}
	session := s.buildSession(ch, enc, id, token, identity)
	session.restored = true
	session.nextSeq, session.peerAck, session.received, session.ackSent = snap.NextSeq, snap.PeerAck, snap.Received, snap.Received
	for _, f := range snap.Outbox {
		out := &outFrame{kind: f.Kind, seq: f.Seq, message: f.Message, replyTo: f.ReplyTo, payload: f.Payload, err: f.Error}
		out.size = len(out.payload) + frameOverhead
		if out.err != nil {
			out.size += len(out.err.Code) + len(out.err.Message)
		}
		session.outbox = append(session.outbox, out)
		session.outboxBytes += out.size
	}
	if s.ImportSession != nil {
		if err := s.ImportSession(session, &session.identity, snap.App); err != nil {
			session.cancel()
			s.observe(Event{Type: "restore_failed", Channel: ch.ID, SessionID: id, UserID: snap.UserID, Reason: "import: " + err.Error()})
			return nil, &Error{Code: CodeSessionExpired, Message: "session cannot be resumed; start a new session", Retryable: true}
		}
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		session.cancel()
		return nil, &Error{Code: CodeServerShutdown, Message: "server is shutting down", Retryable: true}
	}
	if s.sessions[id] != nil {
		s.mu.Unlock()
		session.cancel()
		return nil, &Error{Code: CodeSessionExpired, Message: "session cannot be resumed; start a new session", Retryable: true}
	}
	s.sessions[id] = session
	s.wg.Add(1)
	s.mu.Unlock()
	go session.dispatch()
	return session, nil
}
