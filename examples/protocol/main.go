// A local demo with deterministic responses; it does not call a model provider.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Coder-is/TabForge/protocol"
	"github.com/Coder-is/TabForge/protocol/httptransport"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func main() {
	manifest := flag.String("contract", "examples/protocol/contract.json", "protocol contract")
	address := flag.String("listen", "127.0.0.1:18082", "local demo address")
	flag.Parse()
	c, err := protocol.Load(*manifest)
	if err != nil {
		log.Fatal(err)
	}
	s := httptransport.New(c)
	complete, _ := c.Endpoint("chatComplete")
	stream, _ := c.Endpoint("chatStream")
	makeResponse := func(desc protoreflect.MessageDescriptor, data string) proto.Message {
		m := dynamicpb.NewMessage(desc)
		if err := protojson.Unmarshal([]byte(data), m); err != nil {
			panic(err)
		}
		return m
	}
	if err := s.HandleUnary("chatComplete", func(ctx context.Context, req proto.Message) (proto.Message, error) {
		return makeResponse(complete.Output, `{"text":"你好，TabForge","usage":{"outputTokens":"3"}}`), nil
	}); err != nil {
		log.Fatal(err)
	}
	if err := s.HandleStream("chatStream", func(ctx context.Context, req proto.Message, emit func(proto.Message) error) error {
		for _, data := range []string{
			`{"delta":{"text":"你好，"}}`,
			`{"delta":{"text":"TabForge"}}`,
			`{"usage":{"outputTokens":"3"}}`,
			`{"completed":{"response":{"text":"你好，TabForge","usage":{"outputTokens":"3"}}}}`,
		} {
			if err := emit(makeResponse(stream.Output, data)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
	log.Printf("Demo protocol server: http://%s", *address)
	server := httptransport.HTTPServer(*address, s)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(deadline); err != nil {
			log.Printf("shutdown: %v", err)
			server.Close()
		}
	}()
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
