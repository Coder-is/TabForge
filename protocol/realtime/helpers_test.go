package realtime

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/Coder-is/TabForge/protocol"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

const contractPath = "../../examples/realtime/Generated/protocol/contract.json"

// testContract loads the example and lets a test change channel settings.
func testContract(t testing.TB, edit func(*protocol.Channel)) *protocol.Contract {
	t.Helper()
	c, err := protocol.Load(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if edit == nil {
		return c
	}
	m := c.Manifest
	m.Channels = append([]protocol.Channel(nil), m.Channels...)
	m.Channels[0].Messages = append([]protocol.ChannelMessage(nil), m.Channels[0].Messages...)
	edit(&m.Channels[0])
	data, err := os.ReadFile(strings.TrimSuffix(contractPath, "contract.json") + "schema.pb")
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		t.Fatal(err)
	}
	c, err = protocol.Resolve(m, set)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// proxy forwards TCP connections and can cut them at any moment, the way a
// mobile network drops a connection without a close handshake.
type proxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	refuse   atomic.Bool
}

func newProxy(t testing.TB, target string) *proxy {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{listener: l, target: target, conns: map[net.Conn]struct{}{}}
	go p.serve()
	t.Cleanup(func() { l.Close(); p.cut() })
	return p
}

func (p *proxy) addr() string { return p.listener.Addr().String() }

func (p *proxy) serve() {
	for {
		down, err := p.listener.Accept()
		if err != nil {
			return
		}
		if p.refuse.Load() {
			down.Close()
			continue
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			down.Close()
			continue
		}
		p.mu.Lock()
		p.conns[down], p.conns[up] = struct{}{}, struct{}{}
		p.mu.Unlock()
		pipe := func(dst, src net.Conn) {
			io.Copy(dst, src)
			dst.Close()
			src.Close()
		}
		go pipe(up, down)
		go pipe(down, up)
	}
}

// cut drops every open connection.
func (p *proxy) cut() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.conns) / 2
	for c := range p.conns {
		c.Close()
		delete(p.conns, c)
	}
	return n
}

type fixture struct {
	t      testing.TB
	server *Server
	http   *httptest.Server
	proxy  *proxy
}

func newFixture(t testing.TB, c *protocol.Contract, configure func(*Server)) *fixture {
	t.Helper()
	s := NewServer(c)
	s.Authenticate = func(ctx context.Context, channel, token string) (Identity, error) {
		if !strings.HasPrefix(token, "user-") {
			return Identity{}, errors.New("bad token")
		}
		return Identity{UserID: strings.TrimPrefix(token, "user-")}, nil
	}
	s.AckDelay = 5 * time.Millisecond
	if configure != nil {
		configure(s)
	}
	h := httptest.NewServer(s)
	f := &fixture{t: t, server: s, http: h, proxy: newProxy(t, strings.TrimPrefix(h.URL, "http://"))}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Shutdown(ctx)
		h.Close()
	})
	return f
}

func (f *fixture) client(enc Encoding, user string, direct bool) *Client {
	f.t.Helper()
	addr := f.proxy.addr()
	if direct {
		addr = strings.TrimPrefix(f.http.URL, "http://")
	}
	c, err := NewClient(f.server.Contract, "game", "ws://"+addr+"/ws/game")
	if err != nil {
		f.t.Fatal(err)
	}
	c.Encoding = enc
	c.Token = func() string { return "user-" + user }
	c.MinBackoff, c.MaxBackoff = 5*time.Millisecond, 50*time.Millisecond
	f.t.Cleanup(c.Close)
	return c
}

func connect(t testing.TB, c *Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
}

func encodings(t *testing.T, run func(*testing.T, Encoding)) {
	for _, enc := range []Encoding{EncodingProto, EncodingJSON} {
		t.Run(enc.Subprotocol(), func(t *testing.T) { run(t, enc) })
	}
}

func eventually(t testing.TB, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

var _ = gamepb.Notice_NOTICE_EVENT
var _ = http.StatusOK
var _ = rand.Int
