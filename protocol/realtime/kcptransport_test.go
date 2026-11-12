package realtime

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/coder/websocket"
)

// serveKCP starts the fixture's server on a UDP socket.
func serveKCP(t testing.TB, f *fixture) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.server.ServeKCP(pc) }()
	t.Cleanup(func() {
		pc.Close()
		if err := <-done; err != nil {
			t.Errorf("ServeKCP: %v", err)
		}
	})
	return pc.LocalAddr().String()
}

// udpProxy forwards datagrams with loss, duplication and random delay
// (reordering). rebind gives every client a new source port towards the
// server, the way a NAT mapping changes on a mobile network.
type udpProxy struct {
	t                  testing.TB
	conn               *net.UDPConn
	target             *net.UDPAddr
	mu                 sync.Mutex
	r                  *rand.Rand
	loss, dup          float64
	maxDelay           time.Duration
	upstream           map[string]*net.UDPConn
	rebinds, forwarded atomic.Int64
}

func newUDPProxy(t testing.TB, target string, loss, dup float64, maxDelay time.Duration) *udpProxy {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr, _ := net.ResolveUDPAddr("udp", target)
	p := &udpProxy{t: t, conn: conn, target: addr, r: rand.New(rand.NewSource(13)), loss: loss, dup: dup, maxDelay: maxDelay, upstream: map[string]*net.UDPConn{}}
	go p.serve()
	t.Cleanup(func() {
		conn.Close()
		p.mu.Lock()
		for _, u := range p.upstream {
			u.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *udpProxy) addr() string { return p.conn.LocalAddr().String() }

// forward sends data after a random delay, maybe twice, maybe never.
func (p *udpProxy) forward(write func([]byte)) func([]byte) {
	return func(data []byte) {
		p.mu.Lock()
		copies := 1
		if p.r.Float64() < p.dup {
			copies = 2
		}
		var delays []time.Duration
		for i := 0; i < copies; i++ {
			if p.r.Float64() >= p.loss {
				delays = append(delays, time.Duration(p.r.Int63n(int64(p.maxDelay)+1)))
			}
		}
		p.mu.Unlock()
		for _, d := range delays {
			b := append([]byte(nil), data...)
			time.AfterFunc(d, func() { p.forwarded.Add(1); write(b) })
		}
	}
}

func (p *udpProxy) serve() {
	buf := make([]byte, 64<<10)
	for {
		n, client, err := p.conn.ReadFromUDP(buf)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			continue // Windows: ICMP port unreachable from an earlier send
		}
		up := p.upstreamFor(client)
		if up == nil {
			return
		}
		p.forward(func(b []byte) { up.Write(b) })(buf[:n])
	}
}

func (p *udpProxy) upstreamFor(client *net.UDPAddr) *net.UDPConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	if up := p.upstream[client.String()]; up != nil {
		return up
	}
	up, err := net.DialUDP("udp", nil, p.target)
	if err != nil {
		return nil
	}
	p.upstream[client.String()] = up
	back := p.forward(func(b []byte) { p.conn.WriteToUDP(b, client) })
	go func() {
		buf := make([]byte, 64<<10)
		for {
			n, err := up.Read(buf)
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if err != nil {
				continue
			}
			back(buf[:n])
		}
	}()
	return up
}

func (p *udpProxy) rebind() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, up := range p.upstream {
		up.Close()
		delete(p.upstream, key)
	}
	p.rebinds.Add(1)
}

func kcpClient(t testing.TB, f *fixture, enc Encoding, user, addr string) *Client {
	t.Helper()
	c, err := NewClient(f.server.Contract, "game", "kcp://"+addr+"/ws/game")
	if err != nil {
		t.Fatal(err)
	}
	c.Encoding = enc
	c.Token = func() string { return "user-" + user }
	c.MinBackoff, c.MaxBackoff = 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(c.Close)
	return c
}

func TestKCPRequestPushNotify(t *testing.T) {
	encodings(t, func(t *testing.T, enc Encoding) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		c := kcpClient(t, f, enc, "42", serveKCP(t, f))
		chats := make(chan *gamepb.ChatMessage, 1)
		moved := make(chan *gamepb.PlayerMoved, 8)
		HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) { chats <- m })
		HandlePush(c, "playerMoved", func(m *gamepb.PlayerMoved) { moved <- m })
		connect(t, c)
		ctx := context.Background()
		reply, err := Call[*gamepb.LoginReply](ctx, c, "login", &gamepb.LoginRequest{Nickname: "勇者😀"})
		if err != nil || reply.GetPlayerId() != 42 || reply.GetNickname() != "勇者😀" {
			t.Fatalf("login: %v %v", reply, err)
		}
		if _, err := Call[*gamepb.BuyReply](ctx, c, "buy", &gamepb.BuyRequest{ItemId: 3, Count: 1}); err != nil {
			t.Fatal(err)
		}
		if m := <-chats; m.GetMessageId() != 1 {
			t.Fatalf("push: %v", m)
		}
		// A request far larger than one KCP chunk.
		big := strings.Repeat("长消息 big ", 50000)
		if _, err := Call[*gamepb.ChatAck](ctx, c, "chat", &gamepb.ChatSend{Text: big}); err != nil {
			t.Fatalf("large request: %v", err)
		}
		if _, err := c.Request(ctx, "chat", &gamepb.ChatSend{}); err == nil || err.(*Error).Code != "empty_message" {
			t.Fatalf("business error: %v", err)
		}
		deadline := time.After(5 * time.Second)
		for done := false; !done; {
			c.Notify("move", &gamepb.Move{Position: &gamepb.Vec2{X: 1}, Tick: 4})
			select {
			case m := <-moved:
				done = m.GetTick() == 4
			case <-time.After(20 * time.Millisecond):
			case <-deadline:
				t.Fatal("volatile push not delivered")
			}
		}
	})
}

// TestKCPExactlyOnceUnderLossAndRebinds runs concurrent purchases through
// a proxy that drops, duplicates and reorders datagrams and rebinds the
// client's address at random. KCP repairs the losses; rebinds reset the
// conversation and the session resumes on a new one.
func TestKCPExactlyOnceUnderLossAndRebinds(t *testing.T) {
	encodings(t, func(t *testing.T, enc Encoding) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		proxy := newUDPProxy(t, serveKCP(t, f), 0.1, 0.03, 15*time.Millisecond)
		c := kcpClient(t, f, enc, "7", proxy.addr())
		states := &stateLog{}
		c.OnState = states.record
		var mu sync.Mutex
		var pushes []uint64
		HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) {
			mu.Lock()
			pushes = append(pushes, m.GetMessageId())
			mu.Unlock()
		})
		connect(t, c)
		session := c.SessionID()
		stop := make(chan struct{})
		go func() {
			r := rand.New(rand.NewSource(17))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(300+r.Intn(400)) * time.Millisecond):
					proxy.rebind()
				}
			}
		}()
		deadline := time.Now().Add(2 * time.Second)
		results := make(chan int64, 1<<20)
		var wg sync.WaitGroup
		var failures atomic.Int64
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					reply, err := Call[*gamepb.BuyReply](context.Background(), c, "buy", &gamepb.BuyRequest{ItemId: 1, Count: 1})
					if err != nil {
						failures.Add(1)
						t.Errorf("request failed: %v", err)
						return
					}
					results <- reply.GetGold()
				}
			}()
		}
		wg.Wait()
		close(stop)
		close(results)
		if failures.Load() != 0 {
			states.mu.Lock()
			t.Logf("states: %v", states.states)
			states.mu.Unlock()
			return
		}
		var golds []int
		for g := range results {
			golds = append(golds, int(g))
		}
		sort.Ints(golds)
		for i, g := range golds {
			if g != i+1 {
				t.Fatalf("purchases are not exactly 1..%d: %v", len(golds), golds)
			}
		}
		eventually(t, "all pushes", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(pushes) >= len(golds)
		})
		mu.Lock()
		for i, id := range pushes {
			if id != uint64(i+1) {
				t.Fatalf("push %d has id %d", i, id)
			}
		}
		mu.Unlock()
		if c.SessionID() != session || states.count(string(StateConnected)) != 1 {
			t.Fatalf("session restarted: %v", states.states)
		}
		resumes := states.count(string(StateResumed))
		if proxy.rebinds.Load() < 2 || resumes < 2 {
			t.Fatalf("%d rebinds, %d resumes", proxy.rebinds.Load(), resumes)
		}
		t.Logf("%d purchases, %d rebinds, %d resumes, %d datagrams forwarded", len(golds), proxy.rebinds.Load(), resumes, proxy.forwarded.Load())
	})
}

func TestKCPRefusals(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	addr := serveKCP(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dialKCP(ctx, "kcp://"+addr+"/nowhere", SubprotocolProto, 1<<20); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("unknown path: %v", err)
	}
	// Client.dial relies on a CloseError to treat a refusal as permanent.
	for i := 0; i < 20; i++ {
		_, err := dialKCP(ctx, "kcp://"+addr+"/ws/game", "chat.v2", 1<<20)
		var ce websocket.CloseError
		if !errors.As(err, &ce) || !strings.HasPrefix(ce.Reason, "offer ") {
			t.Fatalf("unknown subprotocol: %v", err)
		}
	}
	// Datagrams for unknown conversations get a reset, never a new session.
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	segment := make([]byte, 24)
	segment[0], segment[4], segment[12] = 9, 81, 5 // conv 9, push, sn 5
	conn.Write(segment)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || n != 5 || buf[0] != 9 || buf[4] != kcpReset {
		t.Fatalf("reset: %v % x", err, buf[:n])
	}
	// Runts get no answer: a 5-byte reset to a 4-byte spoofed datagram
	// would amplify reflected traffic.
	conn.Write([]byte{9, 0, 0, 0})
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := conn.Read(buf); err == nil {
		t.Fatalf("answered a 4-byte datagram: % x", buf[:n])
	}
}
