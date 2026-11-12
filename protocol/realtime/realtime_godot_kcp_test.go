package realtime

import (
	"crypto/ecdh"
	"encoding/base64"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGodotKCPClient runs the GDScript KCP transport (Godot headless): its
// KCP core and crypto against Go-generated cases, the client against
// ServeKCP through a lossy, rebinding UDP proxy (clear text and encrypted,
// both encodings), and the refusals that must stop it. Set GODOT_BIN.
func TestGodotKCPClient(t *testing.T) {
	binary := os.Getenv("GODOT_BIN")
	if binary == "" {
		t.Skip("GODOT_BIN is not set")
	}
	project, _ := filepath.Abs("../../sdk/godot")
	contract, _ := filepath.Abs("../../examples/realtime/Generated/protocol/protocol.gd")
	run := func(args ...string) *exec.Cmd {
		return exec.Command(binary, append([]string{"--headless", "--path", project, "--script", "tests/realtime.gd", "--", args[0], contract}, args[1:]...)...)
	}
	passed := func(t *testing.T, out []byte, err error, mode string) {
		t.Helper()
		if err != nil || strings.Contains(string(out), "SCRIPT ERROR") || !strings.Contains(string(out), "realtime "+mode+": 0 failures") {
			t.Fatalf("Godot %s: %v\n%s", mode, err, out)
		}
	}
	t.Run("crypto", func(t *testing.T) {
		cases := filepath.Join(t.TempDir(), "kcp-cases.json")
		if err := os.WriteFile(cases, kcpCryptoCases(t), 0o644); err != nil {
			t.Fatal(err)
		}
		vectors, _ := filepath.Abs(kcpVectorsFile)
		out, err := run("kcp", cases, vectors).CombinedOutput()
		passed(t, out, err, "kcp")
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "kcp ") {
				t.Log(strings.TrimSpace(line))
			}
		}
	})
	for _, encrypted := range []bool{false, true} {
		for _, encoding := range []string{"proto", "json"} {
			name := map[bool]string{false: "plain", true: "encrypted"}[encrypted] + "-" + encoding
			t.Run(name, func(t *testing.T) {
				var key *ecdh.PrivateKey
				arg := "-"
				if encrypted {
					key = newKCPKey(t)
					arg = base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
				}
				f := newFixture(t, testContract(t, nil), func(s *Server) {
					registerGame(t, s)
					s.KCPKey = key
					s.OnSessionEnd = func(_ *Session, e *Error) {
						if e.Code == CodeProtocolError {
							t.Errorf("server closed a session: %s", e.Message)
						}
					}
				})
				proxy := newUDPProxy(t, serveKCP(t, f), 0.1, 0.03, 15*time.Millisecond)
				proxy.mu.Lock()
				proxy.watch = []byte("user-31")
				if encrypted {
					proxy.corrupt = 0.02
				}
				proxy.mu.Unlock()
				stop := make(chan struct{})
				defer close(stop)
				go func() {
					r := mrand.New(mrand.NewSource(5))
					for {
						select {
						case <-stop:
							return
						case <-time.After(time.Duration(300+r.Intn(400)) * time.Millisecond):
							proxy.rebind()
						}
					}
				}()
				out, err := run("chaos", "kcp://"+proxy.addr()+"/ws/game", encoding, "2", arg).CombinedOutput()
				passed(t, out, err, "chaos")
				if proxy.watched.Load() == encrypted {
					t.Fatalf("token on the wire: %v, encrypted: %v", proxy.watched.Load(), encrypted)
				}
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(line, "{") {
						t.Logf("%s, %d rebinds", strings.TrimSpace(line), proxy.rebinds.Load())
					}
				}
			})
		}
	}
	t.Run("refusals", func(t *testing.T) {
		key := newKCPKey(t)
		encrypted := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
		plain := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		pub := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
		for _, tc := range []struct{ addr, key, text string }{
			{serveKCP(t, encrypted), "-", "only accepts encrypted KCP"},
			{serveKCP(t, plain), pub, "does not offer encrypted KCP"},
		} {
			out, err := run("kcprefuse", "kcp://"+tc.addr+"/ws/game", tc.key, tc.text).CombinedOutput()
			passed(t, out, err, "kcprefuse")
		}
	})
}
