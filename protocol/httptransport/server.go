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
	Contract     *protocol.Contract
	Authorize    Authorize
	MaxBodyBytes int64
	unary        map[string]UnaryHandler
	stream       map[string]StreamHandler
}

func New(c *protocol.Contract) *Server {
	return &Server{Contract: c, MaxBodyBytes: 1 << 20, unary: map[string]UnaryHandler{}, stream: map[string]StreamHandler{}}
}

func (s *Server) HandleUnary(id string, handler UnaryHandler) error {
	e, ok := s.Contract.Endpoint(id)
	if !ok || e.Transport != "http_json" || handler == nil || s.unary[id] != nil {
		return fmt.Errorf("invalid or duplicate unary handler %q", id)
	}
	s.unary[id] = handler
	return nil
}

func (s *Server) HandleStream(id string, handler StreamHandler) error {
	e, ok := s.Contract.Endpoint(id)
	if !ok || e.Transport != "http_sse" || handler == nil || s.stream[id] != nil {
		return fmt.Errorf("invalid or duplicate stream handler %q", id)
	}
	s.stream[id] = handler
	return nil
}

type envelope struct {
	ProtocolVersion string          `json:"protocolVersion"`
	RequestID       string          `json:"requestId"`
	Sequence        string          `json:"sequence,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Error           *Error          `json:"error,omitempty"`
}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		s.fail(w, requestID, 405, "method_not_allowed", "use POST", false)
		return
	}
	if r.Header.Get("X-Protocol-Version") != s.Contract.Manifest.Version {
		s.fail(w, requestID, 409, "version_mismatch", "X-Protocol-Version does not match the contract", false)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		s.fail(w, requestID, 415, "unsupported_media_type", "use application/json with a ProtoJSON request body", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(endpoint.TimeoutMS)*time.Millisecond)
	defer cancel()
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
		s.fail(w, requestID, 400, "bad_request", "could not read bounded request body", false)
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
	handler := s.unary[endpoint.ID]
	if handler == nil {
		s.fail(w, requestID, 500, "internal", "endpoint has no handler", false)
		return
	}
	response, err := handler(ctx, request)
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: requestID, Data: data})
}

func (s *Server) serveStream(ctx context.Context, w http.ResponseWriter, e protocol.ResolvedEndpoint, id string, request proto.Message) {
	handler := s.stream[e.ID]
	flusher, ok := w.(http.Flusher)
	if handler == nil || !ok {
		s.fail(w, id, 500, "internal", "stream handler or HTTP flushing unavailable", false)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flusher.Flush()
	var mu sync.Mutex
	var sequence uint64
	terminal, closed := false, false
	write := func(name string, frame envelope) error {
		sequence++
		frame.Sequence = strconv.FormatUint(sequence, 10)
		data, err := json.Marshal(frame)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", frame.Sequence, name, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
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
		return nil
	}
	err := handler(ctx, request, emit)
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if terminal || ctx.Err() == context.Canceled {
		return
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil {
		err = &Error{Code: "incomplete_stream", Message: "handler ended without a terminal event", Retryable: false}
	}
	_, failure := transportError(err)
	write("protocol.error", envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: id, Error: failure})
}

func (s *Server) marshal(e protocol.ResolvedEndpoint, message proto.Message) ([]byte, error) {
	if message == nil || !message.ProtoReflect().IsValid() || message.ProtoReflect().Descriptor().FullName() != e.Output.FullName() {
		return nil, fmt.Errorf("expected %s", e.Output.FullName())
	}
	return protojson.MarshalOptions{Resolver: s.Contract.Types}.Marshal(message)
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(envelope{ProtocolVersion: s.Contract.Manifest.Version, RequestID: id, Error: &Error{Code: code, Message: message, Retryable: retryable}})
}
