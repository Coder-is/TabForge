package httptransport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	c, err := protocol.Load("../../examples/protocol/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	return New(c)
}
func message(t *testing.T, desc protoreflect.MessageDescriptor, data string) proto.Message {
	t.Helper()
	m := dynamicpb.NewMessage(desc)
	if err := protojson.Unmarshal([]byte(data), m); err != nil {
		t.Fatal(err)
	}
	return m
}
func request(s *Server, path, data string, edit func(*httptest.ResponseRecorder)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Protocol-Version", "1.0.0")
	r.Header.Set("X-Request-ID", "test-request")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if edit != nil {
		edit(w)
	}
	return w
}

func TestUnaryProtoJSONAndInputValidation(t *testing.T) {
	s := fixture(t)
	e, _ := s.Contract.Endpoint("chatComplete")
	if err := s.HandleUnary(e.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		field := req.ProtoReflect().Descriptor().Fields().ByName("conversation_id")
		if req.ProtoReflect().Get(field).Uint() != ^uint64(0) {
			t.Fatal("uint64 lost precision")
		}
		return message(t, e.Output, `{"text":"你好","usage":{"outputTokens":"18446744073709551615"}}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	w := request(s, e.Path, `{"conversationId":"18446744073709551615"}`, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"outputTokens":"18446744073709551615"`) {
		t.Fatalf("invalid reply: %d %s", w.Code, w.Body.String())
	}
	var body envelope
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.RequestID != "test-request" || len(body.Data) == 0 {
		t.Fatalf("invalid envelope: %v", err)
	}
	for _, data := range []string{`{"unknown":true}`, `{"conversationId":"18446744073709551616"}`, `{`, `{ } { }`} {
		w := request(s, e.Path, data, nil)
		if w.Code != 400 {
			t.Fatalf("bad input accepted: %s -> %d", data, w.Code)
		}
	}
	s.MaxBodyBytes = 2
	if w := request(s, e.Path, `{"prompt":"long"}`, nil); w.Code != 400 {
		t.Fatal("request limit not applied")
	}
}

func TestAuthVersionAndMethod(t *testing.T) {
	s := fixture(t)
	// Resolved endpoint options are used by the server after startup.
	s.Contract.Endpoints[0].Auth = "bearer"
	for _, tc := range []struct {
		method, version, auth, content string
		want                           int
	}{
		{"GET", "1.0.0", "", "application/json", 405},
		{"POST", "old", "", "application/json", 409},
		{"POST", "1.0.0", "", "application/json", 401},
		{"POST", "1.0.0", "Bearer invalid", "application/json", 401},
		{"POST", "1.0.0", "Bearer valid", "text/plain", 415},
	} {
		r := httptest.NewRequest(tc.method, "/v1/chat/complete", strings.NewReader("{}"))
		r.Header.Set("X-Protocol-Version", tc.version)
		r.Header.Set("Authorization", tc.auth)
		r.Header.Set("Content-Type", tc.content)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d expected %d: %s", w.Code, tc.want, w.Body.String())
		}
	}
	s.Authorize = func(ctx context.Context, e protocol.ResolvedEndpoint, token string) error {
		if token != "valid" {
			return &Error{Message: "invalid"}
		}
		return nil
	}
	e, _ := s.Contract.Endpoint("chatComplete")
	if err := s.HandleUnary(e.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		return message(t, e.Output, `{}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", e.Path, strings.NewReader("{}"))
	r.Header.Set("X-Protocol-Version", "1.0.0")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer valid")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("authorized request failed: %s", w.Body.String())
	}
}

func TestStreamTerminalAndIncomplete(t *testing.T) {
	for _, complete := range []bool{true, false} {
		s := fixture(t)
		e, _ := s.Contract.Endpoint("chatStream")
		if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
			if err := emit(message(t, e.Output, `{"delta":{"text":"中文"}}`)); err != nil {
				return err
			}
			if complete {
				if err := emit(message(t, e.Output, `{"completed":{"response":{"text":"中文"}}}`)); err != nil {
					return err
				}
				if err := emit(message(t, e.Output, `{"delta":{"text":"late"}}`)); err == nil {
					t.Fatal("event after terminal accepted")
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		w := request(s, e.Path, `{}`, nil)
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
			t.Fatalf("not SSE: %s", w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "id: 1\nevent: text.delta\n") || !strings.Contains(body, `"sequence":"2"`) {
			t.Fatalf("bad framing: %s", body)
		}
		if complete {
			if !strings.Contains(body, "event: completed") || strings.Contains(body, "protocol.error") || strings.Contains(body, "late") {
				t.Fatalf("bad terminal: %s", body)
			}
		} else if !strings.Contains(body, "event: protocol.error") || !strings.Contains(body, "incomplete_stream") {
			t.Fatalf("silent truncation: %s", body)
		}
	}
}

func TestStreamInvalidPayloadAndDeadline(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		s := fixture(t)
		s.Contract.Endpoints[1].TimeoutMS = 5
		e, _ := s.Contract.Endpoint("chatStream")
		if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
			if timeout {
				<-ctx.Done()
				return ctx.Err()
			}
			return emit(message(t, e.Output, `{}`))
		}); err != nil {
			t.Fatal(err)
		}
		w := request(s, e.Path, `{}`, nil)
		if !strings.Contains(w.Body.String(), "event: protocol.error") {
			t.Fatalf("missing error: %s", w.Body.String())
		}
		if timeout && !strings.Contains(w.Body.String(), `"code":"timeout"`) {
			t.Fatal("deadline not reported")
		}
	}
}

func TestTypeScriptClientAgainstGoServer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js 24+ required for cross-language integration")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	major, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(string(version), "v"), ".")[0])
	if major < 24 {
		t.Skip("Node.js 24+ required for native TypeScript integration")
	}
	s := fixture(t)
	u, _ := s.Contract.Endpoint("chatComplete")
	e, _ := s.Contract.Endpoint("chatStream")
	verify := func(req proto.Message) {
		field := req.ProtoReflect().Descriptor().Fields().ByName("conversation_id")
		if req.ProtoReflect().Get(field).Uint() != ^uint64(0) {
			t.Error("uint64 precision lost across TypeScript -> Go")
		}
	}
	if err := s.HandleUnary(u.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		verify(req)
		return message(t, u.Output, `{"text":"你好😀","usage":{"outputTokens":"18446744073709551615"}}`), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		verify(req)
		if err := emit(message(t, e.Output, `{"delta":{"text":"你好😀"}}`)); err != nil {
			return err
		}
		return emit(message(t, e.Output, `{"completed":{"response":{"text":"你好😀"}}}`))
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	command := exec.Command(node, "../../sdk/typescript/integration.mjs", server.URL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cross-language HTTP/SSE: %v\n%s", err, output)
	}
}
