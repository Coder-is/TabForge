package realtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// nativeOrSkip builds the C++ realtime core tests (sdk/native/tests) with
// sanitizers. Set CXX_BIN (for example clang++ or g++) to run them.
func nativeOrSkip(t *testing.T) (string, string) {
	t.Helper()
	cxx := os.Getenv("CXX_BIN")
	if cxx == "" {
		t.Skip("CXX_BIN is not set")
	}
	out := filepath.Join(t.TempDir(), "realtime_test")
	args := []string{"-std=c++17", "-O1", "-g", "-Wall", "-Wextra", "-Werror", "-Wshadow", "-fsanitize=address,undefined", "-fno-sanitize-recover=all", "-pthread",
		"-I", "../../sdk/unreal/Source/TabForgeProtocol/Public", "../../sdk/native/tests/realtime_test.cpp", "-o", out}
	if output, err := exec.Command(cxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", cxx, err, output)
	}
	runtime, _ := filepath.Abs(realtimeRuntime)
	return out, runtime
}

// TestNativeRealtimeClient runs the C++ core used by the Unreal client:
// scripted unit tests, codec equivalence with Go, and the chaos run against
// this server with the WebSocket layer bridged by the test (bridgeNative).
func TestNativeRealtimeClient(t *testing.T) {
	bin, runtime := nativeOrSkip(t)
	t.Run("units", func(t *testing.T) {
		if out, err := exec.Command(bin, "unit", runtime).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("codec", func(t *testing.T) {
		checkCodec(t, "C++", func(input, output string) *exec.Cmd { return exec.Command(bin, "codec", runtime, input, output) })
	})
	t.Run("chaos-proto", func(t *testing.T) {
		t.Log(chaosRun(t, func(url string) ([]byte, error) {
			return bridgeNative(exec.Command(bin, "chaos", runtime, "2"), url)
		}))
	})
}

// bridgeNative runs a native client whose sockets are opened by this process:
// the program writes open/send/close commands to stdout and reads
// opened/message/closed events from stdin (see sdk/native/tests/realtime_test.cpp).
// It returns the program's result lines and stderr.
func bridgeNative(cmd *exec.Cmd, url string) ([]byte, error) {
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
	// A client that stops making progress fails instead of hanging the test.
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
	type socket struct {
		ready  chan struct{}
		ws     *websocket.Conn
		closed bool
	}
	var mu sync.Mutex
	sockets := map[uint64]*socket{}
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
			s := &socket{ready: make(chan struct{})}
			mu.Lock()
			sockets[id] = s
			mu.Unlock()
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{string(data)}})
				cancel()
				mu.Lock()
				s.ws = ws
				closed := s.closed
				mu.Unlock()
				close(s.ready)
				if err != nil {
					emit('c', id, nil)
					return
				}
				if closed {
					ws.CloseNow()
					return
				}
				ws.SetReadLimit(1 << 20)
				emit('o', id, []byte(ws.Subprotocol()))
				for {
					typ, message, err := ws.Read(context.Background())
					if err != nil {
						// The server's error code travels as the close reason.
						reason := ""
						var closeErr websocket.CloseError
						if errors.As(err, &closeErr) && closeErr.Code == websocket.StatusNormalClosure {
							reason = closeErr.Reason
						}
						emit('c', id, []byte(reason))
						return
					}
					op := byte('m')
					if typ == websocket.MessageText {
						op = 't'
					}
					emit(op, id, message)
				}
			}()
		case 'S':
			mu.Lock()
			s := sockets[id]
			mu.Unlock()
			if s == nil {
				continue
			}
			<-s.ready
			if s.ws != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				s.ws.Write(ctx, websocket.MessageBinary, data) // failures surface as a closed read
				cancel()
			}
		case 'C':
			mu.Lock()
			s := sockets[id]
			delete(sockets, id)
			if s != nil {
				s.closed = true
			}
			mu.Unlock()
			if s != nil {
				go func() {
					<-s.ready
					if s.ws != nil {
						s.ws.Close(websocket.StatusNormalClosure, "")
					}
				}()
			}
		case 'L':
			result.Write(data)
			result.WriteByte('\n')
		}
	}
	err = cmd.Wait()
	stdin.Close()
	mu.Lock()
	for _, s := range sockets {
		go func() {
			<-s.ready
			if s.ws != nil {
				s.ws.CloseNow()
			}
		}()
	}
	mu.Unlock()
	out := strings.TrimSpace(result.String())
	if stderr.Len() > 0 {
		out += "\n" + stderr.String()
	}
	return []byte(out), err
}
