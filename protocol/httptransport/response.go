package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Message }

type envelope struct {
	ProtocolVersion string          `json:"protocolVersion"`
	SchemaHash      string          `json:"schemaHash"`
	RequestID       string          `json:"requestId"`
	Sequence        string          `json:"sequence,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Payload         json.RawMessage `json:"payload,omitempty"`
	Error           *Error          `json:"error,omitempty"`
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
