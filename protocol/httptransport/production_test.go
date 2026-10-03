package httptransport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
)

func TestPanicLimitsAndObservation(t *testing.T) {
	s := fixture(t)
	e, _ := s.Contract.Endpoint("chatComplete")
	var result Result
	s.Observe = func(r Result) { result = r }
	if err := s.HandleUnary(e.ID, func(context.Context, proto.Message) (proto.Message, error) { panic("private provider credentials") }); err != nil {
		t.Fatal(err)
	}
	w := request(s, e.Path, `{}`, nil)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") || result.Code != "internal" || result.Bytes == 0 || result.RequestID != "test-request" {
		t.Fatalf("panic/observation: %d %s %+v", w.Code, w.Body.String(), result)
	}
	if w := request(s, e.Path, `{"prompt":"a","prompt":"b"}`, nil); w.Code != 400 {
		t.Fatal("duplicate request member accepted")
	}
	r := httptest.NewRequest("POST", e.Path, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Protocol-Version", "1.0.0")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "schema_mismatch") {
		t.Fatal("missing identity accepted")
	}
}

func TestHeartbeatConcurrentEmitsAndDisconnect(t *testing.T) {
	s := fixture(t)
	e, _ := s.Contract.Endpoint("chatStream")
	s.HeartbeatInterval = time.Millisecond
	disconnected := make(chan struct{})
	if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); emit(message(t, e.Output, `{"delta":{"text":"中文"}}`)) }()
		}
		wg.Wait()
		<-ctx.Done()
		close(disconnected)
		return ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	r, _ := http.NewRequest("POST", server.URL+e.Path, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Protocol-Version", "1.0.0")
	r.Header.Set("X-Protocol-Schema", s.schemaHash)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	// Read enough to establish streaming and then close while the producer waits.
	buffer := make([]byte, 4096)
	if _, err := response.Body.Read(buffer); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("provider context not cancelled on disconnect")
	}
}

func TestCORSPreflight(t *testing.T) {
	handler := WithCORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), []string{"https://game.example"})
	for _, tc := range []struct {
		origin, headers string
		want            int
	}{{"https://game.example", "x-protocol-schema,authorization", 204}, {"https://other.example", "", 403}, {"https://game.example", "x-unknown", 403}} {
		r := httptest.NewRequest("OPTIONS", "/v1/chat", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", tc.headers)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("preflight: %d expected %d", w.Code, tc.want)
		}
		if tc.want == 204 && w.Header().Get("Access-Control-Allow-Origin") != tc.origin {
			t.Fatal("wrong allowed origin")
		}
	}
}

func TestOversizedStreamEnvelopeKeepsErrorSequence(t *testing.T) {
	for _, precedingEvent := range []bool{false, true} {
		s := fixture(t)
		s.MaxResponseBytes = 400
		e, _ := s.Contract.Endpoint("chatStream")
		if err := s.HandleStream(e.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
			if precedingEvent {
				if err := emit(message(t, e.Output, `{"delta":{"text":"ok"}}`)); err != nil {
					return err
				}
			}
			// Payload fits the limit, but its envelope and SSE headers do not.
			return emit(message(t, e.Output, `{"delta":{"text":"`+strings.Repeat("x", 330)+`"}}`))
		}); err != nil {
			t.Fatal(err)
		}
		w := request(s, e.Path, `{}`, nil)
		sequence := "1"
		if precedingEvent {
			sequence = "2"
		}
		if !strings.Contains(w.Body.String(), "id: "+sequence+"\nevent: protocol.error\n") || !strings.Contains(w.Body.String(), `"code":"internal"`) {
			t.Fatalf("missing consecutive error frame: %s", w.Body.String())
		}
		for _, frame := range strings.SplitAfter(w.Body.String(), "\n\n") {
			if len(frame) > s.MaxResponseBytes {
				t.Fatalf("frame exceeds configured bytes: %d", len(frame))
			}
		}
	}
}

func TestHandlerErrorsRespectResponseLimit(t *testing.T) {
	for _, transport := range []string{"http_json", "http_sse"} {
		s := fixture(t)
		s.MaxResponseBytes = 400
		failure := &Error{Code: "bad_request", Message: strings.Repeat("x", 2000)}
		id := "chatComplete"
		if transport == "http_json" {
			if err := s.HandleUnary(id, func(context.Context, proto.Message) (proto.Message, error) { return nil, failure }); err != nil {
				t.Fatal(err)
			}
		} else {
			id = "chatStream"
			if err := s.HandleStream(id, func(context.Context, proto.Message, func(proto.Message) error) error { return failure }); err != nil {
				t.Fatal(err)
			}
		}
		e, _ := s.Contract.Endpoint(id)
		w := request(s, e.Path, `{}`, nil)
		if w.Body.Len() > s.MaxResponseBytes || !strings.Contains(w.Body.String(), `"code":"internal"`) {
			t.Fatalf("unbounded %s error: %s", transport, w.Body.String())
		}
	}
}

func TestGodotUnits(t *testing.T) {
	binary := os.Getenv("GODOT_BIN")
	if binary == "" {
		t.Skip("GODOT_BIN not set")
	}
	project, _ := filepath.Abs("../../sdk/godot")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "--headless", "--path", project, "--script", "tests/unit.gd").CombinedOutput()
	if err != nil || strings.Contains(string(output), "SCRIPT ERROR") || !strings.Contains(string(output), "unit tests: 0 failures") {
		t.Fatalf("Godot units: %v\n%s", err, output)
	}
}
