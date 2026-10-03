// Package httptransport serves validated contracts using ProtoJSON and SSE.
package httptransport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

type UnaryHandler func(context.Context, proto.Message) (proto.Message, error)
type StreamHandler func(context.Context, proto.Message, func(proto.Message) error) error
type Authorize func(context.Context, protocol.ResolvedEndpoint, string) error

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Message }

// Server registrations and options must be configured before serving requests.
// Auth is deny-by-default for bearer endpoints until Authorize is installed.
// Handlers must observe context cancellation/deadlines, including provider calls.
type Server struct {
	Contract          *protocol.Contract
	Authorize         Authorize
	MaxBodyBytes      int64
	MaxResponseBytes  int
	RequireSchemaHash bool
	HeartbeatInterval time.Duration
	Observe           func(Result)
	mu                sync.RWMutex
	schemaHash        string
	unary             map[string]UnaryHandler
	stream            map[string]StreamHandler
}

func New(c *protocol.Contract) *Server {
	return &Server{Contract: c, MaxBodyBytes: 1 << 20, MaxResponseBytes: 1 << 20, RequireSchemaHash: true, HeartbeatInterval: 15 * time.Second, schemaHash: c.Fingerprint(), unary: map[string]UnaryHandler{}, stream: map[string]StreamHandler{}}
}

func (s *Server) HandleUnary(id string, handler UnaryHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.Contract.Endpoint(id)
	if !ok || e.Transport != "http_json" || handler == nil || s.unary[id] != nil {
		return fmt.Errorf("invalid or duplicate unary handler %q", id)
	}
	s.unary[id] = handler
	return nil
}

func (s *Server) HandleStream(id string, handler StreamHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.Contract.Endpoint(id)
	if !ok || e.Transport != "http_sse" || handler == nil || s.stream[id] != nil {
		return fmt.Errorf("invalid or duplicate stream handler %q", id)
	}
	s.stream[id] = handler
	return nil
}

type envelope struct {
	ProtocolVersion string          `json:"protocolVersion"`
	SchemaHash      string          `json:"schemaHash"`
	RequestID       string          `json:"requestId"`
	Sequence        string          `json:"sequence,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Error           *Error          `json:"error,omitempty"`
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	tracked := &responseWriter{ResponseWriter: w}
	w = tracked
	result := Result{}
	defer func() {
		if recovered := recover(); recovered != nil && tracked.status == 0 {
			s.fail(w, result.RequestID, 500, "internal", "request failed", false)
		}
		if s.Observe != nil {
			result.Status = tracked.status
			result.Bytes = tracked.bytes
			result.Duration = time.Since(started)
			result.Code = tracked.code
			result.TerminalEvent = tracked.event
			s.Observe(result)
		}
	}()
	requestID := r.Header.Get("X-Request-ID")
	if requestID == "" {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			s.fail(w, "", http.StatusInternalServerError, "internal", "could not create request ID", false)
			return
		}
		requestID = hex.EncodeToString(id[:])
	}
	if !requestIDPattern.MatchString(requestID) {
		s.fail(w, "", http.StatusBadRequest, "bad_request", "invalid X-Request-ID", false)
		return
	}
	w.Header().Set("X-Request-ID", requestID)
	w.Header().Set("X-Protocol-Version", s.Contract.Manifest.Version)
	w.Header().Set("X-Protocol-Schema", s.schemaHash)
	result.RequestID = requestID
	var endpoint protocol.ResolvedEndpoint
	found := false
	for _, e := range s.Contract.Endpoints {
		if e.Path == r.URL.Path {
			endpoint, found = e, true
			break
		}
	}
	if !found {
		s.fail(w, requestID, 404, "not_found", "unknown endpoint", false)
		return
	}
	result.Endpoint = endpoint.ID
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.fail(w, requestID, 405, "method_not_allowed", "use POST", false)
		return
	}
	if r.Header.Get("X-Protocol-Version") != s.Contract.Manifest.Version {
		s.fail(w, requestID, 409, "version_mismatch", "X-Protocol-Version does not match the contract", false)
		return
	}
	if hash := r.Header.Get("X-Protocol-Schema"); (s.RequireSchemaHash && hash == "") || (hash != "" && hash != s.schemaHash) {
		s.fail(w, requestID, 409, "schema_mismatch", "X-Protocol-Schema does not match the contract", false)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		s.fail(w, requestID, 415, "unsupported_media_type", "use application/json with a ProtoJSON request body", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(endpoint.TimeoutMS)*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	controller := http.NewResponseController(w)
	controller.SetReadDeadline(deadline)
	controller.SetWriteDeadline(deadline)
	defer controller.SetReadDeadline(time.Time{})
	defer controller.SetWriteDeadline(time.Time{})
	if endpoint.Auth == "bearer" {
		header := r.Header.Get("Authorization")
		parts := strings.Fields(header)
		if s.Authorize == nil || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || s.Authorize(ctx, endpoint, parts[1]) != nil {
			s.fail(w, requestID, 401, "unauthorized", "authorization required", false)
			return
		}
	}
	limit := s.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	// protojson needs the complete bounded request; never parse response chunks
	// this way because SSE frames may span arbitrary network reads.
	data, err := ioutil.ReadAll(body)
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			s.fail(w, requestID, 413, "request_too_large", "request body exceeds the configured limit", false)
			return
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			s.fail(w, requestID, 504, "timeout", "request deadline exceeded", true)
			return
		}
		s.fail(w, requestID, 400, "bad_request", "could not read bounded request body", false)
		return
	}
	if err := protocol.ValidateJSON(data); err != nil {
		s.fail(w, requestID, 400, "bad_request", err.Error(), false)
		return
	}
	request := dynamicpb.NewMessage(endpoint.Input)
	if err := (protojson.UnmarshalOptions{Resolver: s.Contract.Types}).Unmarshal(data, request); err != nil {
		s.fail(w, requestID, 400, "bad_request", err.Error(), false)
		return
	}
	if endpoint.Transport == "http_sse" {
		s.serveStream(ctx, w, endpoint, requestID, request)
		return
	}
	s.mu.RLock()
	handler := s.unary[endpoint.ID]
	s.mu.RUnlock()
	if handler == nil {
		s.fail(w, requestID, 500, "internal", "endpoint has no handler", false)
		return
	}
	response, err := invokeUnary(ctx, handler, request)
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		status, failure := transportError(err)
		s.fail(w, requestID, status, failure.Code, failure.Message, failure.Retryable)
		return
	}
	data, err = s.marshal(endpoint, response)
	if err != nil {
		s.fail(w, requestID, 500, "internal", "handler returned an invalid response", false)
		return
	}
	encoded, err := json.Marshal(envelope{ProtocolVersion: s.Contract.Manifest.Version, SchemaHash: s.schemaHash, RequestID: requestID, Data: data})
	if err != nil || len(encoded) > s.responseLimit() {
		s.fail(w, requestID, 500, "internal", "response exceeds the configured limit", false)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(encoded)
}

func (s *Server) serveStream(ctx context.Context, w http.ResponseWriter, e protocol.ResolvedEndpoint, id string, request proto.Message) {
	s.mu.RLock()
	handler := s.stream[e.ID]
	s.mu.RUnlock()
	controller := http.NewResponseController(w)
	if handler == nil || !canFlush(w) {
		s.fail(w, id, 500, "internal", "stream handler or HTTP flushing unavailable", false)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	if err := controller.Flush(); err != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	var sequence uint64
	terminal, closed := false, false
	write := func(name string, frame envelope) error {
		frame.Sequence = strconv.FormatUint(sequence+1, 10)
		frame.SchemaHash = s.schemaHash
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		encoded := fmt.Sprintf("id: %s\nevent: %s\ndata: %s\n\n", frame.Sequence, name, data)
		if len(encoded) > s.responseLimit() {
			return fmt.Errorf("stream frame exceeds response limit")
		}
		if _, err = fmt.Fprint(w, encoded); err != nil {
			cancel()
			return err
		}
		if err := controller.Flush(); err != nil {
			cancel()
			return err
		}
		sequence++
		return nil
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	if s.HeartbeatInterval > 0 {
		go func() {
			defer close(stopped)
			ticker := time.NewTicker(s.HeartbeatInterval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					mu.Lock()
					if !closed && !terminal {
						_, err := fmt.Fprint(w, ": ping\n\n")
						if err == nil {
							err = controller.Flush()
						}
						if err != nil {
							cancel()
						}
					}
					mu.Unlock()
				}
			}
		}()
	} else {
		close(stopped)
	}
	defer func() { close(done); <-stopped }()
	emit := func(response proto.Message) error {
		mu.Lock()
		defer mu.Unlock()
		if closed || terminal {
			return fmt.Errorf("stream is already closed")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		event, err := e.EventFor(response)
		if err != nil {
			return err
		}
		data, err := s.marshal(e, response)
		if err != nil {
			return err
		}
		if err := write(event.Name, envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: id, Payload: data}); err != nil {
			return err
		}
		terminal = event.Terminal
		if terminal {
			if tracked, ok := w.(*responseWriter); ok {
				tracked.event = event.Name
			}
		}
		return nil
	}
	err := invokeStream(ctx, handler, request, emit)
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if terminal || ctx.Err() == context.Canceled {
		if !terminal {
			if tracked, ok := w.(*responseWriter); ok {
				tracked.code = "cancelled"
			}
		}
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = &Error{Code: "incomplete_stream", Message: "handler ended without a terminal event", Retryable: false}
	}
	_, failure := transportError(err)
	if tracked, ok := w.(*responseWriter); ok {
		tracked.code = failure.Code
	}
	if err := write("protocol.error", envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: id, Error: failure}); err != nil && ctx.Err() == nil {
		// A custom handler error can itself exceed the frame limit.
		if tracked, ok := w.(*responseWriter); ok {
			tracked.code = "internal"
		}
		write("protocol.error", envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: id, Error: &Error{Code: "internal", Message: "request failed"}})
	}
}

func (s *Server) marshal(e protocol.ResolvedEndpoint, message proto.Message) ([]byte, error) {
	if message == nil || !message.ProtoReflect().IsValid() || message.ProtoReflect().Descriptor().FullName() != e.Output.FullName() {
		return nil, fmt.Errorf("expected %s", e.Output.FullName())
	}
	data, err := protojson.MarshalOptions{Resolver: s.Contract.Types}.Marshal(message)
	if err != nil {
		return nil, err
	}
	if len(data) > s.responseLimit() {
		return nil, fmt.Errorf("response exceeds configured limit")
	}
	// Reject a handler linked against a different schema with the same full name.
	if err := (protojson.UnmarshalOptions{Resolver: s.Contract.Types}).Unmarshal(data, dynamicpb.NewMessage(e.Output)); err != nil {
		return nil, err
	}
	return data, nil
}

func transportError(err error) (int, *Error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return 504, &Error{Code: "timeout", Message: "request deadline exceeded", Retryable: true}
	}
	var e *Error
	if errors.As(err, &e) {
		switch e.Code {
		case "bad_request":
			return 400, e
		case "unauthorized":
			return 401, e
		default:
			return 500, e
		}
	}
	return 500, &Error{Code: "internal", Message: "request failed", Retryable: false}
}

func (s *Server) fail(w http.ResponseWriter, id string, status int, code, message string, retryable bool) {
	frame := envelope{ProtocolVersion: s.Contract.Manifest.Version, SchemaHash: s.schemaHash, RequestID: id, Error: &Error{Code: code, Message: message, Retryable: retryable}}
	encoded, err := json.Marshal(frame)
	if err != nil || len(encoded) > s.responseLimit() {
		status, code = 500, "internal"
		frame.Error = &Error{Code: code, Message: "request failed"}
		encoded, _ = json.Marshal(frame)
	}
	if tracked, ok := w.(*responseWriter); ok {
		tracked.code = code
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if len(encoded) <= s.responseLimit() {
		w.Write(encoded)
	}
}

func (s *Server) responseLimit() int {
	if s.MaxResponseBytes > 0 {
		return s.MaxResponseBytes
	}
	return 1 << 20
}
func invokeUnary(ctx context.Context, handler UnaryHandler, request proto.Message) (response proto.Message, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = &Error{Code: "internal", Message: "request failed"}
		}
	}()
	return handler(ctx, request)
}
func invokeStream(ctx context.Context, handler StreamHandler, request proto.Message, emit func(proto.Message) error) (err error) {
	defer func() {
		if recover() != nil {
			err = &Error{Code: "internal", Message: "request failed"}
		}
	}()
	return handler(ctx, request, emit)
}
