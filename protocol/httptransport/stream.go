package httptransport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
)

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
