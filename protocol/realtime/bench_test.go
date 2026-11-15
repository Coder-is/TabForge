package realtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
)

// Baselines for the per-message and per-datagram paths. Run with
//
//	go test -run '^$' -bench . -benchmem ./protocol/realtime/

func benchFrame(enc Encoding) *Frame {
	payload := bytes.Repeat([]byte{7}, 64)
	if enc == EncodingJSON {
		payload = []byte(`{"playerId":"42","position":{"x":1.5,"y":-2},"tick":1024}`)
	}
	return &Frame{Kind: KindPush, Seq: 1 << 20, Ack: 1 << 19, Message: 3, Payload: payload}
}

func BenchmarkFrameEncode(b *testing.B) {
	for _, enc := range []Encoding{EncodingProto, EncodingJSON} {
		b.Run(enc.Subprotocol(), func(b *testing.B) {
			f := benchFrame(enc)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := f.Encode(enc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFrameDecode(b *testing.B) {
	for _, enc := range []Encoding{EncodingProto, EncodingJSON} {
		b.Run(enc.Subprotocol(), func(b *testing.B) {
			data, err := benchFrame(enc).Encode(enc)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := DecodeFrame(enc, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkKCPSealOpen seals one 512-byte datagram and opens it again.
func BenchmarkKCPSealOpen(b *testing.B) {
	keys := make([]byte, 2*kcpKeySize)
	rand.Read(keys)
	client, err := kcpCipherFromKeys(9, keys, true)
	if err != nil {
		b.Fatal(err)
	}
	server, err := kcpCipherFromKeys(9, keys, false)
	if err != nil {
		b.Fatal(err)
	}
	plain := make([]byte, 512)
	b.SetBytes(int64(len(plain)))
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := server.open(client.seal(plain)); !ok { // in place, like receive
			b.Fatal("open failed")
		}
	}
}

// BenchmarkKCPAccept is the server's cost of one encrypted hello.
func BenchmarkKCPAccept(b *testing.B) {
	key := newKCPKey(b)
	h, err := newKCPClientHandshake(11, key.PublicKey())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := kcpAccept(key, 11, h.hello); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkKCPLink sends 256-byte messages through two in-memory
// conversations and reads them on the other side.
func BenchmarkKCPLink(b *testing.B) {
	a, z := linkKCP(b, 1<<20)
	ctx := context.Background()
	msg := make([]byte, 256)
	b.SetBytes(int64(len(msg)))
	b.ReportAllocs()
	done := make(chan error, 1)
	n := b.N
	go func() {
		for i := 0; i < n; i++ {
			if _, _, err := z.Read(ctx); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	b.ResetTimer()
	for i := 0; i < n; i++ {
		if err := a.send(ctx, kcpBinary, msg, true); err != nil {
			b.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		b.Fatal(err)
	}
}

// BenchmarkCall measures one request round trip over loopback WebSocket
// and KCP, sequentially and from parallel goroutines sharing a client.
func BenchmarkCall(b *testing.B) {
	for _, transport := range []string{"ws", "kcp"} {
		b.Run(transport, func(b *testing.B) {
			f := newFixture(b, testContract(b, nil), func(s *Server) { registerGame(b, s) })
			var c *Client
			if transport == "kcp" {
				c = kcpClient(b, f, EncodingProto, "1", serveKCP(b, f))
			} else {
				c = f.client(EncodingProto, "1", true)
			}
			connect(b, c)
			ctx := context.Background()
			req := &gamepb.LoginRequest{Nickname: "bench"}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := Call[*gamepb.LoginReply](ctx, c, "login", req); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
