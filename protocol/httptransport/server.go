// Package httptransport serves validated contracts using ProtoJSON and SSE.
package httptransport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
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
	return &Server{
		Contract:          c,
		MaxBodyBytes:      1 << 20,
		MaxResponseBytes:  1 << 20,
		RequireSchemaHash: true,
		HeartbeatInterval: 15 * time.Second,
		schemaHash:        c.Fingerprint(),
		unary:             make(map[string]UnaryHandler),
		stream:            make(map[string]StreamHandler),
	}
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
	data, err := io.ReadAll(body)
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
	s.serveUnary(ctx, w, endpoint, requestID, request)
}

func (s *Server) serveUnary(ctx context.Context, w http.ResponseWriter, endpoint protocol.ResolvedEndpoint, requestID string, request proto.Message) {
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
	data, err := s.marshal(endpoint, response)
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
