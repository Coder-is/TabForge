package realtime

import (
	"math/rand"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// chaos runs a client program against this server while the proxy drops
// connections at random for the whole run, including the volatile and kick
// checks at the end. It returns the program output.
func chaos(t *testing.T, command func(url string) *exec.Cmd) string {
	t.Helper()
	f := newFixture(t, testContract(t, nil), func(s *Server) {
		registerGame(t, s)
		// Node's WebSocket negotiates permessage-deflate, so the TS client
		// runs compressed (almost every frame, given the low threshold);
		// the .NET and Godot clients do not offer it and fall back.
		s.Compression = CompressionContextTakeover
		s.CompressionThreshold = 16
		s.Observe = func(e Event) {
			if e.Type == "handshake_reject" && e.Code == CodeProtocolError {
				t.Errorf("server rejected a handshake: %s", e.Reason)
			}
		}
		s.OnSessionEnd = func(_ *Session, e *Error) {
			if e.Code == CodeProtocolError {
				t.Errorf("server closed a session: %s", e.Message)
			}
		}
	})
	stop := make(chan struct{})
	var cuts atomic.Int64
	go func() {
		r := rand.New(rand.NewSource(3))
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(5+r.Intn(40)) * time.Millisecond):
				cuts.Add(int64(f.proxy.cut()))
			}
		}
	}()
	out, err := command("ws://" + f.proxy.addr() + "/ws/game").CombinedOutput()
	close(stop)
	if err != nil {
		t.Fatalf("client: %v\n%s", err, out)
	}
	if cuts.Load() < 10 {
		t.Fatalf("only %d cuts", cuts.Load())
	}
	return strings.TrimSpace(string(out)) + " (" + itoa(cuts.Load()) + " cuts)"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestTypeScriptClientUnderChaos runs the TypeScript client against this
// server while the proxy drops connections at random.
func TestTypeScriptClientUnderChaos(t *testing.T) {
	node := nodeOrSkip(t)
	for _, encoding := range []string{"proto", "json"} {
		t.Run(encoding, func(t *testing.T) {
			t.Log(chaos(t, func(url string) *exec.Cmd {
				return exec.Command(node, "../../sdk/typescript/realtime.integration.mjs", url, encoding, "2")
			}))
		})
	}
}
