// Package platformtest provides deliberate HTTP fault fixtures for SDK acceptance.
// It is not a production model-provider implementation.
package platformtest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/protocol/httptransport"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

type Server struct {
	Handler   http.Handler
	Cancelled atomic.Int64
}

func New(contract *protocol.Contract) (*Server, error) {
	server := &Server{}
	transport := httptransport.New(contract)
	complete, ok := contract.Endpoint("chatComplete")
	if !ok {
		return nil, fmt.Errorf("acceptance contract requires chatComplete")
	}
	stream, ok := contract.Endpoint("chatStream")
	if !ok {
		return nil, fmt.Errorf("acceptance contract requires chatStream")
	}
	message := func(desc protoreflect.MessageDescriptor, value interface{}) proto.Message {
		data, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		m := dynamicpb.NewMessage(desc)
		if err := protojson.Unmarshal(data, m); err != nil {
			panic(err)
		}
		return m
	}
	if err := transport.HandleUnary(complete.ID, func(ctx context.Context, req proto.Message) (proto.Message, error) {
		return message(complete.Output, map[string]interface{}{"text": "你好😀", "usage": map[string]string{"outputTokens": "18446744073709551615"}}), nil
	}); err != nil {
		return nil, err
	}
	if err := transport.HandleStream(stream.ID, func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		prompt := req.ProtoReflect().Get(req.ProtoReflect().Descriptor().Fields().ByName("prompt")).String()
		if err := emit(message(stream.Output, map[string]interface{}{"delta": map[string]string{"text": "你好😀"}})); err != nil {
			return err
		}
		if prompt == "cancel" || prompt == "timeout" {
			<-ctx.Done()
			server.Cancelled.Add(1)
			return ctx.Err()
		}
		if prompt == "truncated" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(75 * time.Millisecond):
		}
		return emit(message(stream.Output, map[string]interface{}{"completed": map[string]interface{}{"response": map[string]string{"text": "你好😀"}}}))
	}); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/", transport)
	mux.HandleFunc("/_platform/fault/", func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimPrefix(r.URL.Path, "/_platform/fault/")
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Protocol-Version", contract.Manifest.Version)
		w.Header().Set("X-Protocol-Schema", contract.Fingerprint())
		frame := map[string]interface{}{"protocolVersion": contract.Manifest.Version, "schemaHash": contract.Fingerprint(), "requestId": r.Header.Get("X-Request-ID"), "sequence": "1", "payload": map[string]interface{}{"delta": map[string]interface{}{"text": "你好😀"}}}
		sequence := "1"
		switch kind {
		case "gap":
			sequence = "2"
			frame["sequence"] = sequence
		case "schema":
			frame["schemaHash"] = "different"
		case "type":
			frame["payload"] = map[string]interface{}{"delta": map[string]interface{}{"text": 42}}
		case "large":
			frame["payload"] = map[string]interface{}{"delta": map[string]interface{}{"text": strings.Repeat("x", 2<<20)}}
		case "utf8":
			fmt.Fprint(w, "id: 1\nevent: text.delta\ndata: \xed\xa0\x80\n\n")
			return
		}
		data, _ := json.Marshal(frame)
		fmt.Fprintf(w, "id: %s\nevent: text.delta\ndata: %s\n\n", sequence, data)
	})
	server.Handler = mux
	return server, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler.ServeHTTP(w, r) }
