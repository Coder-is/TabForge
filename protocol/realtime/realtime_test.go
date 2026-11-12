package realtime

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func registerGame(t testing.TB, s *Server) {
	t.Helper()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(OnRequest(s, "game", "login", func(ctx context.Context, session *Session, req *gamepb.LoginRequest) (*gamepb.LoginReply, error) {
		id, _ := strconv.ParseUint(session.Identity().UserID, 10, 64)
		return &gamepb.LoginReply{PlayerId: id, Nickname: req.GetNickname(), Gold: 100}, nil
	}))
	must(OnRequest(s, "game", "buy", func(ctx context.Context, session *Session, req *gamepb.BuyRequest) (*gamepb.BuyReply, error) {
		counter, _ := session.Values.LoadOrStore("purchases", new(atomic.Int64))
		n := counter.(*atomic.Int64).Add(1)
		if err := session.Push("chatMessage", &gamepb.ChatMessage{MessageId: uint64(n), Text: "bought", SentAt: timestamppb.New(time.Unix(1700000000, 5))}); err != nil {
			return nil, err
		}
		return &gamepb.BuyReply{Gold: n, Inventory: map[uint32]uint32{req.GetItemId(): req.GetCount()}}, nil
	}))
	must(OnRequest(s, "game", "chat", func(ctx context.Context, session *Session, req *gamepb.ChatSend) (*gamepb.ChatAck, error) {
		if req.GetText() == "" {
			return nil, &Error{Code: "empty_message", Message: "say something"}
		}
		if req.GetText() == "kick" {
			time.AfterFunc(20*time.Millisecond, func() { session.Close(CodeKicked, "bye") })
		}
		if req.GetText() == "panic" {
			panic("handler bug")
		}
		if req.GetText() == "slow" {
			select {
			case <-time.After(300 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &gamepb.ChatAck{MessageId: 7}, nil
	}))
	must(OnNotify(s, "game", "move", func(ctx context.Context, session *Session, m *gamepb.Move) {
		session.Push("playerMoved", &gamepb.PlayerMoved{PlayerId: 1, Position: m.GetPosition(), Tick: m.GetTick()})
	}))
}

func TestRequestPushNotify(t *testing.T) {
	encodings(t, func(t *testing.T, enc Encoding) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		c := f.client(enc, "42", true)
		moved := make(chan *gamepb.PlayerMoved, 1)
		chats := make(chan *gamepb.ChatMessage, 1)
		if err := HandlePush(c, "playerMoved", func(m *gamepb.PlayerMoved) { moved <- m }); err != nil {
			t.Fatal(err)
		}
		if err := HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) { chats <- m }); err != nil {
			t.Fatal(err)
		}
		connect(t, c)
		ctx := context.Background()
		reply, err := Call[*gamepb.LoginReply](ctx, c, "login", &gamepb.LoginRequest{Nickname: "勇者😀"})
		if err != nil || reply.GetPlayerId() != 42 || reply.GetNickname() != "勇者😀" || reply.GetGold() != 100 {
			t.Fatalf("login: %v %v", reply, err)
		}
		buy, err := Call[*gamepb.BuyReply](ctx, c, "buy", &gamepb.BuyRequest{ItemId: 4294967295, Count: 3})
		if err != nil || buy.GetInventory()[4294967295] != 3 {
			t.Fatalf("buy: %v %v", buy, err)
		}
		if m := <-chats; m.GetMessageId() != 1 || m.GetSentAt().AsTime() != time.Unix(1700000000, 5).UTC() {
			t.Fatalf("push: %v", m)
		}
		if err := c.Notify("move", &gamepb.Move{Position: &gamepb.Vec2{X: 1.5, Y: -2}, Tick: 9}); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-moved:
			if m.GetTick() != 9 || m.GetPosition().GetX() != 1.5 {
				t.Fatalf("moved: %v", m)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("volatile push not delivered")
		}
		// Application errors, handler panics and untyped calls.
		if _, err := c.Request(ctx, "chat", &gamepb.ChatSend{}); err == nil || err.(*Error).Code != "empty_message" {
			t.Fatalf("business error: %v", err)
		}
		if _, err := c.Request(ctx, "chat", &gamepb.ChatSend{Text: "panic"}); err == nil || err.(*Error).Code != CodeInternal {
			t.Fatalf("panic: %v", err)
		}
		if _, err := c.Request(ctx, "joinRoom", &gamepb.JoinRoomRequest{}); err == nil || err.(*Error).Code != CodeNotImplemented {
			t.Fatalf("missing handler: %v", err)
		}
		if _, err := c.Request(ctx, "chat", &gamepb.LoginRequest{}); err == nil {
			t.Fatal("wrong request type accepted")
		}
		dynamic, err := c.Request(ctx, "chat", &gamepb.ChatSend{Text: "hi"})
		if err != nil || dynamic.ProtoReflect().Descriptor().FullName() != "tabforge.game.ChatAck" {
			t.Fatalf("untyped call: %v %v", dynamic, err)
		}
	})
}

// TestExactlyOnceUnderChaos cuts the TCP connection at random moments while
// requests and pushes are in flight. Every request must run exactly once,
// every response must arrive, and pushes must arrive once and in order.
func TestExactlyOnceUnderChaos(t *testing.T) {
	encodings(t, func(t *testing.T, enc Encoding) {
		f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		c := f.client(enc, "7", false)
		var mu sync.Mutex
		var pushes []uint64
		HandlePush(c, "chatMessage", func(m *gamepb.ChatMessage) {
			mu.Lock()
			pushes = append(pushes, m.GetMessageId())
			mu.Unlock()
		})
		var states sync.Map
		c.OnState = func(s State, _ *Error) {
			n, _ := states.LoadOrStore(s, new(atomic.Int64))
			n.(*atomic.Int64).Add(1)
		}
		connect(t, c)
		session := c.SessionID()
		stop := make(chan struct{})
		var cuts atomic.Int64
		go func() {
			r := rand.New(rand.NewSource(1))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(2+r.Intn(30)) * time.Millisecond):
					cuts.Add(int64(f.proxy.cut()))
				}
			}
		}()
		const workers = 8
		deadline := time.Now().Add(2 * time.Second)
		results := make(chan int64, 1<<20)
		var wg sync.WaitGroup
		var failures atomic.Int64
		for w := 0; w < workers; w++ {
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
			return
		}
		var golds []int
		for g := range results {
			golds = append(golds, int(g))
		}
		total := len(golds)
		sort.Ints(golds)
		for i, g := range golds {
			if g != i+1 {
				t.Fatalf("request results are not exactly 1..%d (duplicate or lost execution): %v", total, golds)
			}
		}
		eventually(t, "all pushes", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(pushes) >= total
		})
		mu.Lock()
		for i, id := range pushes {
			if id != uint64(i+1) {
				t.Fatalf("push %d has id %d: pushes lost, duplicated or reordered", i, id)
			}
		}
		if len(pushes) != total {
			t.Fatalf("got %d pushes", len(pushes))
		}
		mu.Unlock()
		if c.SessionID() != session {
			t.Fatal("session was replaced instead of resumed")
		}
		resumed, _ := states.Load(StateResumed)
		if cuts.Load() < 20 || resumed == nil || resumed.(*atomic.Int64).Load() < 20 {
			t.Fatalf("chaos did not exercise resume: %d cuts", cuts.Load())
		}
		t.Logf("%d requests, %d cuts, %d resumes", total, cuts.Load(), resumed.(*atomic.Int64).Load())
		// Once acknowledged, nothing stays buffered on the server.
		eventually(t, "server buffer drained", func() bool {
			s, ok := f.server.Session(session)
			if !ok {
				return false
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			return len(s.outbox) == 0
		})
	})
}

func TestSessionExpiryStartsNewSession(t *testing.T) {
	contract := testContract(t, func(ch *protocol.Channel) { ch.Resume = 200 })
	f := newFixture(t, contract, func(s *Server) { registerGame(t, s) })
	c := f.client(EncodingProto, "1", false)
	states := make(chan State, 32)
	c.OnState = func(s State, _ *Error) { states <- s }
	connect(t, c)
	first := c.SessionID()
	// A request whose handler is running when the network drops for longer
	// than the resume window: its outcome is unknown, so it fails.
	started := make(chan struct{})
	f.server.mu.Lock()
	f.server.channels["game"].requests["joinRoom"] = &requestRoute{message: mustMessage(t, contract, "joinRoom"), newIn: func() proto.Message { return &gamepb.JoinRoomRequest{} }, handler: func(ctx context.Context, s *Session, m proto.Message) (proto.Message, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	f.server.mu.Unlock()
	lost := make(chan error, 1)
	go func() {
		_, err := c.Request(context.Background(), "joinRoom", &gamepb.JoinRoomRequest{Room: "a"})
		lost <- err
	}()
	<-started
	f.proxy.refuse.Store(true)
	f.proxy.cut()
	eventually(t, "server session expiry", func() bool { _, ok := f.server.Session(first); return !ok })
	// Queued while offline: never written, so it carries over to the new session.
	queued := make(chan error, 1)
	go func() {
		_, err := Call[*gamepb.ChatAck](context.Background(), c, "chat", &gamepb.ChatSend{Text: "queued"})
		queued <- err
	}()
	time.Sleep(50 * time.Millisecond)
	f.proxy.refuse.Store(false)
	if err := <-lost; err == nil || err.(*Error).Code != CodeSessionLost {
		t.Fatalf("in-flight request: %v", err)
	}
	if err := <-queued; err != nil {
		t.Fatalf("queued request: %v", err)
	}
	if c.SessionID() == first || c.SessionID() == "" {
		t.Fatal("no new session")
	}
	seen := map[State]bool{}
	for len(states) > 0 {
		seen[<-states] = true
	}
	if !seen[StateConnected] || !seen[StateDisconnected] {
		t.Fatalf("states: %v", seen)
	}
}

func mustMessage(t testing.TB, c *protocol.Contract, name string) protocol.ResolvedMessage {
	ch, _ := c.Channel("game")
	m, ok := ch.Message(name)
	if !ok {
		t.Fatal(name)
	}
	return m
}

func TestKickAndDuplicateLogin(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) {
		registerGame(t, s)
		s.SingleSessionPerUser = true
	})
	first := f.client(EncodingProto, "5", true)
	connect(t, first)
	second := f.client(EncodingJSON, "5", true)
	connect(t, second)
	eventually(t, "first client closed", func() bool { return first.Err() != nil })
	if first.Err().(*Error).Code != CodeDuplicateLogin {
		t.Fatalf("duplicate login: %v", first.Err())
	}
	if _, err := first.Request(context.Background(), "chat", &gamepb.ChatSend{Text: "x"}); err == nil {
		t.Fatal("closed client accepted a request")
	}
	if n := len(f.server.Sessions("5")); n != 1 {
		t.Fatalf("%d sessions for one user", n)
	}
	s, _ := f.server.Session(second.SessionID())
	s.Close(CodeKicked, "bye")
	eventually(t, "kicked", func() bool { return second.Err() != nil })
	if e := second.Err().(*Error); e.Code != CodeKicked || e.Message != "bye" {
		t.Fatalf("kick: %v", e)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.server.Sessions("")); n != 0 {
		t.Fatalf("kicked client reconnected: %d sessions", n)
	}
}

func TestHandshakeRejections(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	c := f.client(EncodingProto, "1", true)
	c.Token = func() string { return "forged" }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err == nil || err.(*Error).Code != CodeUnauthorized {
		t.Fatalf("bad token: %v", err)
	}
	for name, hello := range map[string]*Hello{
		CodeVersionMismatch: {ProtocolVersion: "9.9.9", SchemaHash: f.server.schemaHash, Token: "user-1"},
		CodeSchemaMismatch:  {ProtocolVersion: "1.0.0", SchemaHash: "other", Token: "user-1"},
		CodeUnauthorized:    {ProtocolVersion: "1.0.0", SchemaHash: f.server.schemaHash},
		CodeSessionExpired:  {ProtocolVersion: "1.0.0", SchemaHash: f.server.schemaHash, SessionID: "missing", ResumeToken: "x"},
	} {
		raw := dialRaw(t, f, EncodingJSON)
		raw.send(&Frame{Kind: KindHello, Hello: hello})
		if got := raw.read(); got.Kind != KindClose || got.Error.Code != name {
			t.Fatalf("%s: got %+v", name, got)
		}
	}
	// Resume with another session's id but a wrong token.
	good := f.client(EncodingJSON, "1", true)
	connect(t, good)
	raw := dialRaw(t, f, EncodingJSON)
	raw.send(&Frame{Kind: KindHello, Hello: &Hello{ProtocolVersion: "1.0.0", SchemaHash: f.server.schemaHash, SessionID: good.SessionID(), ResumeToken: "guess"}})
	if got := raw.read(); got.Kind != KindClose || got.Error.Code != CodeSessionExpired {
		t.Fatalf("hijack: %+v", got)
	}
	// Wrong subprotocol and non-hello first frames.
	if ws, _, err := websocket.Dial(ctx, "ws"+f.http.URL[4:]+"/ws/game", &websocket.DialOptions{Subprotocols: []string{"other"}}); err == nil {
		if _, _, err := ws.Read(ctx); err == nil {
			t.Fatal("unknown subprotocol accepted")
		}
	}
	raw = dialRaw(t, f, EncodingProto)
	raw.send(&Frame{Kind: KindPing})
	if got := raw.read(); got.Kind != KindClose || got.Error.Code != CodeProtocolError {
		t.Fatalf("ping first: %+v", got)
	}
}

type rawConn struct {
	t   testing.TB
	ws  *websocket.Conn
	enc Encoding
}

func dialRaw(t testing.TB, f *fixture, enc Encoding) *rawConn {
	t.Helper()
	ws, _, err := websocket.Dial(context.Background(), "ws"+f.http.URL[4:]+"/ws/game", &websocket.DialOptions{Subprotocols: []string{enc.Subprotocol()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return &rawConn{t: t, ws: ws, enc: enc}
}

func (r *rawConn) send(f *Frame) {
	data, err := f.Encode(r.enc)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.ws.Write(ctx, messageType(r.enc), data); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rawConn) sendBytes(data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.ws.Write(ctx, messageType(r.enc), data)
}

// read returns the next non-ack frame.
func (r *rawConn) read() *Frame {
	r.t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, data, err := r.ws.Read(ctx)
		cancel()
		if err != nil {
			r.t.Fatalf("read: %v", err)
		}
		f, err := DecodeFrame(r.enc, data)
		if err != nil {
			r.t.Fatalf("decode: %v", err)
		}
		if f.Kind != KindAck {
			return f
		}
	}
}

func (r *rawConn) hello(f *fixture) *Welcome {
	r.send(&Frame{Kind: KindHello, Hello: &Hello{ProtocolVersion: "1.0.0", SchemaHash: f.server.schemaHash, Token: "user-9"}})
	w := r.read()
	if w.Kind != KindWelcome {
		r.t.Fatalf("expected welcome, got %+v", w)
	}
	return w.Welcome
}

func TestProtocolViolationsCloseTheSession(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	payload, _ := proto.Marshal(&gamepb.ChatSend{Text: "x"})
	cases := map[string][]*Frame{
		"sequence gap":       {{Kind: KindRequest, Seq: 2, Message: 3, Payload: payload}},
		"push from client":   {{Kind: KindPush, Seq: 1, Message: 201, Payload: payload}},
		"request as notify":  {{Kind: KindNotify, Seq: 1, Message: 3, Payload: payload}},
		"unknown message":    {{Kind: KindRequest, Seq: 1, Message: 999, Payload: payload}},
		"sequenced volatile": {{Kind: KindNotify, Seq: 1, Message: 100}},
		"volatile reliable":  {{Kind: KindNotify, Message: 100}, {Kind: KindRequest, Message: 3}},
		"ack from future":    {{Kind: KindAck, Ack: 5}},
		"second hello":       {{Kind: KindHello, Hello: &Hello{ProtocolVersion: "1.0.0"}}},
	}
	for name, frames := range cases {
		t.Run(name, func(t *testing.T) {
			raw := dialRaw(t, f, EncodingProto)
			raw.hello(f)
			for _, frame := range frames {
				raw.send(frame)
			}
			got := raw.read()
			for got.Kind == KindPush { // "volatile reliable": the move may be echoed before the close
				got = raw.read()
			}
			if got.Kind != KindClose || got.Error.Code != CodeProtocolError {
				t.Fatalf("got %+v %+v", got, got.Error)
			}
		})
	}
	t.Run("garbage", func(t *testing.T) {
		raw := dialRaw(t, f, EncodingJSON)
		raw.hello(f)
		raw.sendBytes([]byte(`{"kind":"request","seq":"1","message":3,"payload":{},"payload":{}}`))
		if got := raw.read(); got.Kind != KindClose || got.Error.Code != CodeProtocolError {
			t.Fatalf("duplicate member accepted: %+v", got)
		}
	})
	t.Run("bad payload answers bad_request", func(t *testing.T) {
		raw := dialRaw(t, f, EncodingJSON)
		raw.hello(f)
		raw.sendBytes([]byte(`{"kind":"request","seq":"1","message":3,"payload":{"text":5}}`))
		got := raw.read()
		if got.Kind != KindResponse || got.ReplyTo != 1 || got.Error == nil || got.Error.Code != CodeBadRequest {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("duplicate request is not executed twice", func(t *testing.T) {
		raw := dialRaw(t, f, EncodingProto)
		raw.hello(f)
		buy, _ := proto.Marshal(&gamepb.BuyRequest{ItemId: 1, Count: 1})
		raw.send(&Frame{Kind: KindRequest, Seq: 1, Message: 4, Payload: buy})
		raw.send(&Frame{Kind: KindRequest, Seq: 1, Message: 4, Payload: buy})
		raw.send(&Frame{Kind: KindRequest, Seq: 2, Message: 4, Payload: buy})
		var golds []int64
		for len(golds) < 2 {
			f := raw.read()
			if f.Kind == KindResponse {
				reply := &gamepb.BuyReply{}
				proto.Unmarshal(f.Payload, reply)
				golds = append(golds, reply.GetGold())
			}
		}
		if golds[0] != 1 || golds[1] != 2 {
			t.Fatalf("golds %v", golds)
		}
	})
}

func TestSlowConsumerIsClosed(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) {
		registerGame(t, s)
		s.MaxUnackedFrames = 16
	})
	ended := make(chan *Error, 1)
	f.server.OnSessionEnd = func(s *Session, e *Error) { ended <- e }
	raw := dialRaw(t, f, EncodingProto)
	w := raw.hello(f)
	s, _ := f.server.Session(w.SessionID)
	var err error
	for i := 0; i < 100 && err == nil; i++ {
		err = s.Push("chatMessage", &gamepb.ChatMessage{MessageId: uint64(i)})
	}
	if err != ErrSessionClosed {
		t.Fatalf("push to an unacknowledging client: %v", err)
	}
	if e := <-ended; e.Code != CodeOverloaded {
		t.Fatalf("end: %v", e)
	}
	if err := s.Push("chatMessage", &gamepb.ChatMessage{}); err != ErrSessionClosed {
		t.Fatal(err)
	}
}

func TestHeartbeatTimeoutDetachesAndExpires(t *testing.T) {
	contract := testContract(t, func(ch *protocol.Channel) { ch.Heartbeat, ch.Resume = 1000, 500 })
	f := newFixture(t, contract, nil)
	events := make(chan string, 16)
	f.server.Observe = func(e Event) { events <- e.Type }
	raw := dialRaw(t, f, EncodingProto)
	w := raw.hello(f)
	if w.HeartbeatMS != 1000 || w.ResumeWindowMS != 500 {
		t.Fatalf("welcome %+v", w)
	}
	started := time.Now()
	// Silent client: no pings.
	for e := range events {
		if e == "session_end" {
			break
		}
	}
	if elapsed := time.Since(started); elapsed < 3*time.Second || elapsed > 8*time.Second {
		t.Fatalf("idle session ended after %v", elapsed)
	}
	// A pinging client stays connected through the same period.
	c := f.client(EncodingProto, "1", true)
	connect(t, c)
	id := c.SessionID()
	time.Sleep(5 * time.Second)
	if s, ok := f.server.Session(id); !ok || !s.Connected() {
		t.Fatal("heartbeats did not keep the session alive")
	}
}

func TestShutdownTellsClients(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	c := f.client(EncodingProto, "1", true)
	states := make(chan string, 16)
	c.OnState = func(s State, e *Error) {
		if e != nil {
			states <- string(s) + ":" + e.Code
		}
	}
	connect(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-states; got != "disconnected:"+CodeServerShutdown {
		t.Fatalf("state %s", got)
	}
	if c.Err() != nil {
		t.Fatal("retryable shutdown closed the client permanently")
	}
}

func TestGroupBroadcast(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	group := NewGroup()
	f.server.OnSessionStart = group.Add
	var received atomic.Int64
	clients := []*Client{}
	for i, enc := range []Encoding{EncodingProto, EncodingJSON, EncodingProto} {
		c := f.client(enc, fmt.Sprint(i), true)
		HandlePush(c, "notice", func(n *gamepb.ServerNotice) {
			if n.GetKind() == gamepb.Notice_NOTICE_MAINTENANCE && n.GetMinutesLeft() == 5 {
				received.Add(1)
			}
		})
		connect(t, c)
		clients = append(clients, c)
	}
	eventually(t, "group", func() bool { return group.Len() == 3 })
	first, _ := f.server.Session(clients[0].SessionID())
	n, err := group.Push("notice", &gamepb.ServerNotice{Kind: gamepb.Notice_NOTICE_MAINTENANCE, Detail: &gamepb.ServerNotice_MinutesLeft{MinutesLeft: 5}}, func(s *Session) bool { return s == first })
	if err != nil || n != 2 {
		t.Fatalf("broadcast: %d %v", n, err)
	}
	eventually(t, "broadcast delivered", func() bool { return received.Load() == 2 })
	clients[1].Close()
	eventually(t, "member removed", func() bool { return group.Len() == 2 })
}

func TestTypedHandlersRejectForeignSchemas(t *testing.T) {
	s := NewServer(testContract(t, nil))
	if err := OnRequest(s, "game", "login", func(ctx context.Context, _ *Session, req *gamepb.ChatSend) (*gamepb.LoginReply, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("request type mismatch accepted")
	}
	if err := OnRequest(s, "game", "login", func(ctx context.Context, _ *Session, req *gamepb.LoginRequest) (*gamepb.ChatAck, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("response type mismatch accepted")
	}
	if err := OnRequest(s, "game", "move", func(ctx context.Context, _ *Session, req *gamepb.Move) (*gamepb.ChatAck, error) { return nil, nil }); err == nil {
		t.Fatal("notify registered as request")
	}
	if err := s.HandleRequest("game", "login", func(context.Context, *Session, proto.Message) (proto.Message, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleRequest("game", "login", func(context.Context, *Session, proto.Message) (proto.Message, error) { return nil, nil }); err == nil {
		t.Fatal("duplicate handler accepted")
	}
}

// A kick whose close frame is lost with the connection must still stop the
// client: its resume attempt is answered with the original reason.
func TestKickSurvivesLostCloseFrame(t *testing.T) {
	f := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
	c := f.client(EncodingProto, "3", false)
	connect(t, c)
	s, _ := f.server.Session(c.SessionID())
	f.proxy.refuse.Store(true)
	f.proxy.cut()
	s.Close(CodeKicked, "cheating")
	time.Sleep(50 * time.Millisecond)
	f.proxy.refuse.Store(false)
	eventually(t, "client closed", func() bool { return c.Err() != nil })
	if e := c.Err().(*Error); e.Code != CodeKicked || e.Message != "cheating" {
		t.Fatalf("got %v", e)
	}
	if n := len(f.server.Sessions("3")); n != 0 {
		t.Fatalf("kicked player started %d new sessions", n)
	}
}
