package realtime

import (
	"math/rand"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestTypeScriptClientUnderChaos runs the TypeScript client against this
// server while the proxy drops connections at random.
func TestTypeScriptClientUnderChaos(t *testing.T) {
	node := nodeOrSkip(t)
	for _, encoding := range []string{"proto", "json"} {
		t.Run(encoding, func(t *testing.T) {
			f := newFixture(t, testContract(t, nil), func(s *Server) {
				registerGame(t, s)
				s.Observe = func(e Event) {
					if e.Type == "session_end" || e.Type == "session_start" {
						t.Logf("%s %s %s", e.Type, e.SessionID[:6], e.Code)
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
			// Stop cutting before the volatile and kick checks at the end.
			go func() { time.Sleep(2500 * time.Millisecond); close(stop) }()
			out, err := exec.Command(node, "../../sdk/typescript/realtime.integration.mjs", "ws://"+f.proxy.addr()+"/ws/game", encoding, "2").CombinedOutput()
			if err != nil {
				t.Fatalf("node: %v\n%s", err, out)
			}
			if cuts.Load() < 10 {
				t.Fatalf("only %d cuts", cuts.Load())
			}
			t.Logf("%s (%d cuts)", strings.TrimSpace(string(out)), cuts.Load())
		})
	}
}
