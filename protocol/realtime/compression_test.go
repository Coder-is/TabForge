package realtime

import (
	"context"
	"strings"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/coder/websocket"
)

// TestCompressionNegotiation checks every combination of server and client
// settings: compression is used only when both sides enable it, and
// requests, responses and pushes round-trip either way.
func TestCompressionNegotiation(t *testing.T) {
	modes := []Compression{CompressionDisabled, CompressionContextTakeover, CompressionNoContextTakeover}
	big := strings.Repeat("压缩测试 compression ", 20000) // ~460 KB, very repetitive
	encodings(t, func(t *testing.T, enc Encoding) {
		for _, serverMode := range modes {
			for _, clientMode := range modes {
				f := newFixture(t, testContract(t, nil), func(s *Server) {
					registerGame(t, s)
					s.Compression = serverMode
				})
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				ws, resp, err := websocket.Dial(ctx, "ws"+f.http.URL[4:]+"/ws/game", &websocket.DialOptions{
					Subprotocols: []string{enc.Subprotocol()}, CompressionMode: clientMode.mode(),
				})
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				negotiated := strings.Contains(resp.Header.Get("Sec-WebSocket-Extensions"), "permessage-deflate")
				ws.CloseNow()
				if want := serverMode != CompressionDisabled && clientMode != CompressionDisabled; negotiated != want {
					t.Fatalf("server %d client %d: negotiated=%v", serverMode, clientMode, negotiated)
				}

				c := f.client(enc, "7", true)
				c.Compression = clientMode
				chats := make(chan *gamepb.ChatMessage, 1)
				if err := HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) { chats <- m }); err != nil {
					t.Fatal(err)
				}
				connect(t, c)
				reply, err := Call[*gamepb.LoginReply](context.Background(), c, "login", &gamepb.LoginRequest{Nickname: big[:3000]})
				if err != nil || reply.GetNickname() != big[:3000] {
					t.Fatalf("server %d client %d login: %v", serverMode, clientMode, err)
				}
				if _, err := Call[*gamepb.ChatAck](context.Background(), c, "chat", &gamepb.ChatSend{Text: big}); err != nil {
					t.Fatalf("server %d client %d large request: %v", serverMode, clientMode, err)
				}
				if _, err := Call[*gamepb.BuyReply](context.Background(), c, "buy", &gamepb.BuyRequest{ItemId: 1, Count: 1}); err != nil {
					t.Fatal(err)
				}
				if m := <-chats; m.GetText() != "bought" {
					t.Fatalf("push: %v", m)
				}
				c.Close()
			}
		}
	})
}

// TestCompressedMessageLimitAppliesAfterInflate sends a small compressed
// hello that inflates beyond MaxFrameBytes. The same hello is accepted when
// the limit is large enough, so the rejection comes from the inflated size.
func TestCompressedMessageLimitAppliesAfterInflate(t *testing.T) {
	for _, tc := range []struct {
		limit  int
		accept bool
	}{{1 << 20, true}, {64 << 10, false}} {
		f := newFixture(t, testContract(t, nil), func(s *Server) {
			s.Compression = CompressionContextTakeover
			s.MaxFrameBytes = tc.limit
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ws, _, err := websocket.Dial(ctx, "ws"+f.http.URL[4:]+"/ws/game", &websocket.DialOptions{
			Subprotocols: []string{SubprotocolJSON}, CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			t.Fatal(err)
		}
		hello := &Frame{Kind: KindHello, Hello: &Hello{
			ProtocolVersion: f.server.Contract.Manifest.Version,
			SchemaHash:      f.server.schemaHash,
			Token:           "user-" + strings.Repeat("a", 512<<10), // ~1 KB compressed
		}}
		data, err := hello.Encode(EncodingJSON)
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
		_, reply, err := ws.Read(ctx)
		ws.CloseNow()
		cancel()
		var welcomed bool
		if err == nil {
			frame, decodeErr := DecodeFrame(EncodingJSON, reply)
			welcomed = decodeErr == nil && frame.Kind == KindWelcome
		}
		if welcomed != tc.accept {
			t.Fatalf("limit %d: welcomed=%v (read err %v, close %v)", tc.limit, welcomed, err, websocket.CloseStatus(err))
		}
		if !tc.accept && err == nil {
			t.Fatalf("limit %d: server answered an oversized hello: %s", tc.limit, reply)
		}
	}
}
