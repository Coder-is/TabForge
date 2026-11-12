package realtime

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// TestCSharpKCPClient runs the Unity SDK's KCP transport (plain .NET):
// its KCP core and crypto against Go-generated cases, the C# client against
// ServeKCP through a lossy, rebinding UDP proxy (plain and encrypted, both
// encodings), and the refusals that must stop it instead of retrying.
func TestCSharpKCPClient(t *testing.T) {
	cmd := dotnetOrSkip(t)
	run := func(args ...string) *exec.Cmd {
		return exec.Command(cmd[0], append([]string{cmd[1], args[0], cmd[2]}, args[1:]...)...)
	}
	t.Run("crypto", func(t *testing.T) {
		cases := filepath.Join(t.TempDir(), "kcp-cases.json")
		if err := os.WriteFile(cases, kcpCryptoCases(t), 0o644); err != nil {
			t.Fatal(err)
		}
		vectors, _ := filepath.Abs(kcpVectorsFile)
		out, err := run("kcp", cases, vectors).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
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
				proxy.watch = []byte("user-31") // the C# chaos client's token
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
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
				var result struct{ Requests, Resumes int }
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil || result.Requests == 0 || result.Resumes == 0 {
					t.Fatalf("no requests or no resumes across %d rebinds: %s", proxy.rebinds.Load(), out)
				}
				if proxy.watched.Load() == encrypted {
					t.Fatalf("token on the wire: %v, encrypted: %v", proxy.watched.Load(), encrypted)
				}
				t.Logf("%d requests, %d rebinds, %d resumes", result.Requests, proxy.rebinds.Load(), result.Resumes)
			})
		}
	}
	t.Run("refusals", func(t *testing.T) {
		key := newKCPKey(t)
		encrypted := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
		plain := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		pub := base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
		for _, tc := range []struct{ name, addr, key, text string }{
			{"plain client, encrypting server", serveKCP(t, encrypted), "-", "only accepts encrypted KCP"},
			{"encrypting client, plain server", serveKCP(t, plain), pub, "does not offer encrypted KCP"},
		} {
			out, err := run("kcprefuse", "kcp://"+tc.addr+"/ws/game", tc.key, tc.text).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", tc.name, err, out)
			}
		}
	})
}

// kcpCryptoCases generates X25519 and ChaCha20-Poly1305 cases with Go's
// implementations for the C# ones to reproduce.
func kcpCryptoCases(t *testing.T) []byte {
	t.Helper()
	random := func(n int) []byte {
		b := make([]byte, n)
		rand.Read(b)
		return b
	}
	type x25519Case struct {
		Scalar string `json:"scalar"`
		Point  string `json:"point"`
		Out    string `json:"out"`
	}
	type aeadCase struct {
		Key    string `json:"key"`
		Nonce  string `json:"nonce"`
		Aad    string `json:"aad"`
		Plain  string `json:"plain"`
		Sealed string `json:"sealed"`
	}
	var cases struct {
		X25519 []x25519Case `json:"x25519"`
		Aead   []aeadCase   `json:"aead"`
	}
	for len(cases.X25519) < 300 {
		scalar, point := random(32), random(32) // any 32 bytes, including non-canonical points
		if len(cases.X25519)%3 == 0 {
			k, _ := ecdh.X25519().GenerateKey(rand.Reader)
			point = k.PublicKey().Bytes()
		}
		priv, err := ecdh.X25519().NewPrivateKey(scalar)
		if err != nil {
			t.Fatal(err)
		}
		pub, err := ecdh.X25519().NewPublicKey(point)
		if err != nil {
			t.Fatal(err)
		}
		out, err := priv.ECDH(pub)
		if err != nil {
			continue // low-order point; covered separately in C#
		}
		cases.X25519 = append(cases.X25519, x25519Case{hex.EncodeToString(scalar), hex.EncodeToString(point), hex.EncodeToString(out)})
	}
	lengths := []int{0, 1, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 1147, 1200}
	for i := 0; i < 200; i++ {
		n := mrand.Intn(300)
		if i < len(lengths) {
			n = lengths[i]
		}
		key, nonce, aad, plain := random(32), random(12), random(mrand.Intn(40)), random(n)
		aead, err := chacha20poly1305.New(key)
		if err != nil {
			t.Fatal(err)
		}
		cases.Aead = append(cases.Aead, aeadCase{hex.EncodeToString(key), hex.EncodeToString(nonce), hex.EncodeToString(aad), hex.EncodeToString(plain), hex.EncodeToString(aead.Seal(nil, nonce, plain, aad))})
	}
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(fmt.Errorf("cases: %w", err))
	}
	return data
}
