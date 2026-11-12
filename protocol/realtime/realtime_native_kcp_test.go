package realtime

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	mrand "math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestNativeKCPClient runs the C++ KCP transport used by the Unreal client
// (TabForgeKcp.h, built with sanitizers): crypto against Go-generated cases
// and the fixed-key vectors, two cores over a lossy virtual link, the
// session engine over KCP against ServeKCP through the lossy, rebinding UDP
// proxy (clear text and encrypted; the native client speaks Protobuf only),
// and the refusals that must stop it. The test opens the UDP sockets and
// relays datagrams (bridgeNativeKCP), as the engine's FSocket layer would.
func TestNativeKCPClient(t *testing.T) {
	bin, runtime := nativeOrSkip(t)
	t.Run("crypto", func(t *testing.T) {
		cases := filepath.Join(t.TempDir(), "kcp-cases.json")
		if err := os.WriteFile(cases, kcpCryptoCases(t), 0o644); err != nil {
			t.Fatal(err)
		}
		vectors, _ := filepath.Abs(kcpVectorsFile)
		out, err := exec.Command(bin, "kcpcrypto", runtime, cases, vectors).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	})
	t.Run("core", func(t *testing.T) {
		out, err := exec.Command(bin, "kcpcore", runtime).CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
	})
	for _, encrypted := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "encrypted"}[encrypted], func(t *testing.T) {
			var key *ecdh.PrivateKey
			arg := "-"
			if encrypted {
				key = newKCPKey(t)
				arg = hex.EncodeToString(key.PublicKey().Bytes())
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
			url := "kcp://" + proxy.addr() + "/ws/game"
			out, err := bridgeNativeKCP(exec.Command(bin, "kcpchaos", runtime, url, arg, "2"), proxy.addr())
			if err != nil {
				t.Fatalf("client: %v\n%s", err, out)
			}
			if proxy.watched.Load() == encrypted {
				t.Fatalf("token on the wire: %v, encrypted: %v", proxy.watched.Load(), encrypted)
			}
			t.Logf("%s, %d rebinds", strings.TrimSpace(string(out)), proxy.rebinds.Load())
		})
	}
	t.Run("refusals", func(t *testing.T) {
		key := newKCPKey(t)
		encrypted := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s); s.KCPKey = key })
		plain := newFixture(t, testContract(t, nil), func(s *Server) { registerGame(t, s) })
		for _, tc := range []struct{ addr, key, text string }{
			{serveKCP(t, encrypted), "-", "only accepts encrypted KCP"},
			{serveKCP(t, plain), hex.EncodeToString(key.PublicKey().Bytes()), "does not offer encrypted KCP"},
		} {
			out, err := bridgeNativeKCP(exec.Command(bin, "kcprefuse", runtime, "kcp://"+tc.addr+"/ws/game", tc.key, tc.text), tc.addr)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		}
	})
}

// bridgeNativeKCP runs a native client whose UDP sockets this process opens:
// the program writes 'O' (open a socket for an id), 'U' (send a datagram)
// and 'C' (close) to stdout, and reads 'u' (a received datagram) and 'c'
// (the socket failed) from stdin, with the framing of bridgeNative. Every
// socket is connected to target. It returns the result lines and stderr.
func bridgeNativeKCP(cmd *exec.Cmd, target string) ([]byte, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	watchdog := time.AfterFunc(2*time.Minute, func() { cmd.Process.Kill() })
	defer watchdog.Stop()
	var writeMu sync.Mutex
	emit := func(op byte, id uint64, data []byte) {
		writeMu.Lock()
		defer writeMu.Unlock()
		var header [13]byte
		header[0] = op
		binary.LittleEndian.PutUint64(header[1:], id)
		binary.LittleEndian.PutUint32(header[9:], uint32(len(data)))
		stdin.Write(header[:])
		stdin.Write(data)
	}
	var mu sync.Mutex
	sockets := map[uint64]net.Conn{}
	var result bytes.Buffer
	reader := bufio.NewReader(stdout)
	for {
		var header [13]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			break
		}
		id := binary.LittleEndian.Uint64(header[1:])
		data := make([]byte, binary.LittleEndian.Uint32(header[9:]))
		if _, err := io.ReadFull(reader, data); err != nil {
			break
		}
		switch header[0] {
		case 'O':
			conn, err := net.Dial("udp", target)
			if err != nil {
				emit('c', id, nil)
				continue
			}
			mu.Lock()
			sockets[id] = conn
			mu.Unlock()
			go func() {
				buf := make([]byte, 64<<10)
				for {
					n, err := conn.Read(buf)
					if err != nil {
						if !isClosed(err) {
							continue // Windows: ICMP port unreachable from an earlier send
						}
						return
					}
					emit('u', id, append([]byte(nil), buf[:n]...))
				}
			}()
		case 'U':
			mu.Lock()
			conn := sockets[id]
			mu.Unlock()
			if conn != nil {
				conn.Write(data)
			}
		case 'C':
			mu.Lock()
			if conn := sockets[id]; conn != nil {
				conn.Close()
				delete(sockets, id)
			}
			mu.Unlock()
		case 'L':
			result.Write(data)
			result.WriteByte('\n')
		}
	}
	err = cmd.Wait()
	stdin.Close()
	mu.Lock()
	for _, conn := range sockets {
		conn.Close()
	}
	mu.Unlock()
	return append(result.Bytes(), stderr.Bytes()...), err
}

func isClosed(err error) bool { return errors.Is(err, net.ErrClosed) }
