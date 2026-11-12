package realtime

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gamepb "github.com/Coder-is/TabForge/examples/realtime/Generated/schema/go"
	"github.com/Coder-is/TabForge/protocol"
	"github.com/coder/websocket"
)

// migratable configures a server to hand sessions over through store,
// carrying the purchase counter that registerGame keeps in Values.
func migratable(t testing.TB, store SessionStore, events *eventLog) func(*Server) {
	return func(s *Server) {
		registerGame(t, s)
		s.Store = store
		s.ExportSession = func(session *Session) ([]byte, error) {
			n := int64(0)
			if v, ok := session.Values.Load("purchases"); ok {
				n = v.(*atomic.Int64).Load()
			}
			return []byte(strconv.FormatInt(n, 10)), nil
		}
		s.ImportSession = func(session *Session, identity *Identity, state []byte) error {
			n, err := strconv.ParseInt(string(state), 10, 64)
			if err != nil {
				return err
			}
			counter := new(atomic.Int64)
			counter.Store(n)
			session.Values.Store("purchases", counter)
			identity.Data = "imported"
			return nil
		}
		if events != nil {
			s.Observe = events.add
		}
	}
}

type eventLog struct {
	mu     sync.Mutex
	events []Event
}

func (l *eventLog) add(e Event) {
	l.mu.Lock()
	l.events = append(l.events, e)
	l.mu.Unlock()
}

func (l *eventLog) count(typ string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func shutdown(t testing.TB, s *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func addr(f *fixture) string { return strings.TrimPrefix(f.http.URL, "http://") }

type stateLog struct {
	mu     sync.Mutex
	states []string
}

func (l *stateLog) record(s State, e *Error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e != nil {
		l.states = append(l.states, string(s)+":"+e.Code)
		return
	}
	l.states = append(l.states, string(s))
}

func (l *stateLog) count(s string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, state := range l.states {
		if state == s {
			n++
		}
	}
	return n
}

func (l *stateLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.states) == 0 {
		return ""
	}
	return l.states[len(l.states)-1]
}

// TestRollingDeployKeepsSessions replaces the server twice (A -> B -> C)
// while the client buys in a loop and the network drops at random. Every
// purchase must run exactly once, with the counter continuing across
// servers, pushes must stay in order, and the session must never restart.
func TestRollingDeployKeepsSessions(t *testing.T) {
	encodings(t, func(t *testing.T, enc Encoding) {
		store := NewMemoryStore()
		events := &eventLog{}
		contract := testContract(t, nil)
		a := newFixture(t, contract, migratable(t, store, events))
		b := newFixture(t, contract, migratable(t, store, events))
		c3 := newFixture(t, contract, migratable(t, store, events))
		var identities sync.Map
		for _, f := range []*fixture{b, c3} {
			f.server.OnSessionStart = func(s *Session) {
				if !s.Restored() {
					t.Errorf("a new session started instead of a restore")
				}
				identities.Store(s.Identity().Data, true)
			}
		}
		c := a.client(enc, "7", false)
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
		var chaos sync.WaitGroup
		chaos.Add(1)
		go func() {
			defer chaos.Done()
			r := rand.New(rand.NewSource(5))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(10+r.Intn(60)) * time.Millisecond):
					a.proxy.cut()
				}
			}
		}()
		results := make(chan int64, 1<<20)
		var failures atomic.Int64
		var workers sync.WaitGroup
		for w := 0; w < 8; w++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
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
		time.Sleep(400 * time.Millisecond)
		a.proxy.retarget(addr(b))
		shutdown(t, a.server)
		time.Sleep(400 * time.Millisecond)
		a.proxy.retarget(addr(c3))
		shutdown(t, b.server)
		time.Sleep(400 * time.Millisecond)
		close(stop)
		workers.Wait()
		chaos.Wait()
		close(results)
		if failures.Load() != 0 {
			return
		}
		var golds []int
		for g := range results {
			golds = append(golds, int(g))
		}
		sort.Ints(golds)
		for i, g := range golds {
			if g != i+1 {
				t.Fatalf("purchases are not exactly 1..%d across servers: %v", len(golds), golds)
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
				t.Fatalf("push %d has id %d: lost, duplicated or reordered across servers", i, id)
			}
		}
		mu.Unlock()
		if c.SessionID() != session || states.count(string(StateConnected)) != 1 {
			t.Fatalf("session restarted: %v", states.states)
		}
		if events.count("session_handoff") != 2 || events.count("session_restore") != 2 {
			t.Fatalf("handoffs %d, restores %d", events.count("session_handoff"), events.count("session_restore"))
		}
		if _, ok := identities.Load("imported"); !ok {
			t.Fatal("ImportSession did not set identity data")
		}
		if store.Len() != 0 {
			t.Fatalf("store still holds %d entries", store.Len())
		}
		t.Logf("%d purchases over 3 servers, %d resumes", len(golds), states.count(string(StateResumed)))
	})
}

// TestHandOffWaitsForRunningHandlers starts a slow request and shuts the
// server down while it runs: the response is delivered by the next server.
func TestHandOffWaitsForRunningHandlers(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, migratable(t, store, nil))
	var ended atomic.Value
	a.server.OnSessionEnd = func(_ *Session, e *Error) { ended.Store(e.Code) }
	c := a.client(EncodingProto, "7", false)
	connect(t, c)
	done := make(chan error, 1)
	go func() {
		reply, err := Call[*gamepb.ChatAck](context.Background(), c, "chat", &gamepb.ChatSend{Text: "slow"})
		if err == nil && reply.GetMessageId() != 7 {
			err = errors.New("wrong reply")
		}
		done <- err
	}()
	eventually(t, "request running", func() bool {
		s, ok := a.server.Session(c.SessionID())
		if !ok {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.received == 1
	})
	// Take the client offline while the handler runs, so the response can
	// only reach it through the snapshot on the next server.
	a.proxy.refuse.Store(true)
	a.proxy.cut()
	a.proxy.retarget(addr(b))
	shutdown(t, a.server)
	if ended.Load() != CodeHandedOff {
		t.Fatalf("old server reported %v", ended.Load())
	}
	a.proxy.refuse.Store(false)
	if err := <-done; err != nil {
		t.Fatalf("slow request across hand-off: %v", err)
	}
	if _, ok := b.server.Session(c.SessionID()); !ok {
		t.Fatal("session not on the new server")
	}
}

// TestHandOffNeedsTheResumeToken checks a snapshot cannot be taken (or
// destroyed) without the token, and that a server with another schema
// refuses it.
func TestHandOffNeedsTheResumeToken(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, func(s *Server) { migratable(t, store, nil)(s); s.HandoffGrace = -1 }) // expects session_expired at once
	c := a.client(EncodingJSON, "7", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	session := c.SessionID()
	a.proxy.refuse.Store(true) // Keep the client away until we are done.
	shutdown(t, a.server)
	if store.Len() != 1 {
		t.Fatalf("store has %d entries", store.Len())
	}
	// A forged resume with the right id but a wrong token.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr(b)+"/ws/game", &websocket.DialOptions{Subprotocols: []string{SubprotocolJSON}})
	if err != nil {
		t.Fatal(err)
	}
	hello, _ := (&Frame{Kind: KindHello, Hello: &Hello{ProtocolVersion: contract.Manifest.Version, SchemaHash: b.server.schemaHash, SessionID: session, ResumeToken: "forged"}}).Encode(EncodingJSON)
	ws.Write(ctx, websocket.MessageText, hello)
	_, data, err := ws.Read(ctx)
	ws.CloseNow()
	if err != nil {
		t.Fatal(err)
	}
	if f, err := DecodeFrame(EncodingJSON, data); err != nil || f.Kind != KindClose || f.Error.Code != CodeSessionExpired {
		t.Fatalf("forged resume: %s", data)
	}
	if store.Len() != 1 {
		t.Fatal("a forged resume removed the snapshot")
	}
	a.proxy.retarget(addr(b))
	a.proxy.refuse.Store(false)
	eventually(t, "resume on b", func() bool { return states.last() == string(StateResumed) })
	if c.SessionID() != session {
		t.Fatal("session replaced")
	}

}

// TestHandOffRefusesAnotherSchema: a server whose contract differs (and
// that accepts hellos without a schema hash) must not continue a session
// whose frames were encoded for the old schema.
func TestHandOffRefusesAnotherSchema(t *testing.T) {
	store := NewMemoryStore()
	a := newFixture(t, testContract(t, nil), migratable(t, store, nil))
	b := newFixture(t, testContract(t, func(ch *protocol.Channel) { ch.Heartbeat++ }), func(s *Server) {
		migratable(t, store, nil)(s)
		s.RequireSchemaHash = false
	})
	c := a.client(EncodingJSON, "7", false)
	connect(t, c)
	c.mu.Lock()
	session, token := c.sessionID, c.resumeToken
	c.mu.Unlock()
	a.proxy.refuse.Store(true)
	shutdown(t, a.server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws://"+addr(b)+"/ws/game", &websocket.DialOptions{Subprotocols: []string{SubprotocolJSON}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	hello, _ := (&Frame{Kind: KindHello, Hello: &Hello{ProtocolVersion: b.server.Contract.Manifest.Version, SessionID: session, ResumeToken: token}}).Encode(EncodingJSON)
	ws.Write(ctx, websocket.MessageText, hello)
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f, err := DecodeFrame(EncodingJSON, data); err != nil || f.Kind != KindClose || f.Error.Code != CodeSessionExpired {
		t.Fatalf("resume on another schema: %s", data)
	}
}

// TestKickReasonSurvivesHandOff kicks a session whose close frame is lost,
// then replaces the server: the client still learns it was kicked.
func TestKickReasonSurvivesHandOff(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, migratable(t, store, nil))
	c := a.client(EncodingProto, "7", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	session, _ := a.server.Session(c.SessionID())
	a.proxy.refuse.Store(true)
	a.proxy.cut()
	eventually(t, "detached", func() bool { return !session.Connected() })
	session.Close(CodeKicked, "bye")
	shutdown(t, a.server)
	a.proxy.retarget(addr(b))
	a.proxy.refuse.Store(false)
	eventually(t, "closed", func() bool { return states.last() == string(StateClosed)+":"+CodeKicked })
}

type failingStore struct{ *MemoryStore }

func (failingStore) Put(context.Context, string, []byte, time.Duration) error {
	return errors.New("store unavailable")
}

// TestFailedHandOffStartsNewSession: when the store cannot save a session,
// the client gets a new session (connected), as without a store.
func TestFailedHandOffStartsNewSession(t *testing.T) {
	contract := testContract(t, nil)
	events := &eventLog{}
	store := &failingStore{MemoryStore: NewMemoryStore()}
	a := newFixture(t, contract, migratable(t, store, events))
	b := newFixture(t, contract, func(s *Server) { migratable(t, store, nil)(s); s.HandoffGrace = -1 }) // expects session_expired at once
	c := a.client(EncodingProto, "7", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	session := c.SessionID()
	a.proxy.retarget(addr(b))
	shutdown(t, a.server)
	eventually(t, "new session", func() bool { return states.count(string(StateConnected)) == 2 })
	if c.SessionID() == session || events.count("handoff_failed") != 1 {
		t.Fatalf("session %v, failures %d", c.SessionID() == session, events.count("handoff_failed"))
	}
}

// TestResumeDuringHandOffIsRetried lets the client reach the next server
// while the old one is still draining a slow handler: the next server must
// ask it to retry rather than start a new session.
func TestResumeDuringHandOffIsRetried(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	events := &eventLog{}
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, migratable(t, store, events))
	c := a.client(EncodingProto, "7", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	done := make(chan error, 1)
	go func() {
		_, err := Call[*gamepb.ChatAck](context.Background(), c, "chat", &gamepb.ChatSend{Text: "slow"})
		done <- err
	}()
	eventually(t, "request running", func() bool {
		s, ok := a.server.Session(c.SessionID())
		if !ok {
			return false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.received == 1
	})
	a.proxy.retarget(addr(b))
	stopped := make(chan struct{})
	go func() { shutdown(t, a.server); close(stopped) }()
	eventually(t, "drain started", func() bool { return store.Len() == 1 }) // the moving marker
	a.proxy.cut()                                                           // the client now reaches b mid-drain
	if err := <-done; err != nil {
		t.Fatalf("request: %v", err)
	}
	<-stopped
	if states.count(string(StateConnected)) != 1 || states.last() != string(StateResumed) {
		t.Fatalf("states %v", states.states)
	}
	if events.count("session_restore") != 1 {
		t.Fatalf("restores %d", events.count("session_restore"))
	}
}

// TestHandoffGraceCoversLateShutdown: traffic moves to B before A starts
// handing off, and the client reconnects in between. Within HandoffGrace B
// tells it to retry instead of expiring the session; once A shuts down, B
// restores the session and the request sent meanwhile completes once.
func TestHandoffGraceCoversLateShutdown(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, migratable(t, store, nil))
	c := a.client(EncodingProto, "8", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	session := c.SessionID()
	if reply, err := Call[*gamepb.BuyReply](context.Background(), c, "buy", &gamepb.BuyRequest{ItemId: 1, Count: 1}); err != nil || reply.GetGold() != 1 {
		t.Fatalf("first purchase: %v %v", reply, err)
	}
	a.proxy.retarget(addr(b))
	a.proxy.cut()
	a.proxy.cut()
	done := make(chan error, 1)
	var gold int64
	go func() {
		reply, err := Call[*gamepb.BuyReply](context.Background(), c, "buy", &gamepb.BuyRequest{ItemId: 1, Count: 1})
		gold = reply.GetGold()
		done <- err
	}()
	time.Sleep(300 * time.Millisecond) // the client keeps reaching B, which has nothing yet
	shutdown(t, a.server)
	select {
	case err := <-done:
		if err != nil || gold != 2 {
			t.Fatalf("purchase across the late hand-off: gold %d, %v (states %v)", gold, err, states.states)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("purchase never completed (states %v)", states.states)
	}
	if c.SessionID() != session || states.count(string(StateConnected)) != 1 {
		t.Fatalf("session restarted: %v", states.states)
	}
}

// TestHandoffGraceExpires: if no hand-off comes, the session expires once
// the grace has passed, and the client starts a new one.
func TestHandoffGraceExpires(t *testing.T) {
	store := NewMemoryStore()
	contract := testContract(t, nil)
	a := newFixture(t, contract, migratable(t, store, nil))
	b := newFixture(t, contract, func(s *Server) { migratable(t, store, nil)(s); s.HandoffGrace = 400 * time.Millisecond })
	c := a.client(EncodingProto, "9", false)
	states := &stateLog{}
	c.OnState = states.record
	connect(t, c)
	session := c.SessionID()
	// B serves something first, so its grace starts now.
	warm := b.client(EncodingProto, "10", true)
	connect(t, warm)
	start := time.Now()
	a.proxy.retarget(addr(b))
	a.proxy.cut()
	a.proxy.cut()
	eventually(t, "a new session on B", func() bool { return c.SessionID() != session && c.SessionID() != "" })
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("session expired after %v, within the grace", elapsed)
	}
	if states.count(string(StateConnected)) != 2 {
		t.Fatalf("states %v", states.states)
	}
}
