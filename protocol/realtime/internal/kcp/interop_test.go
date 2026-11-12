package kcp

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Upstream ikcp 2.1.1 (github.com/skywind3000/kcp, commit fbd716b2).
const ikcpSHA256 = "8654038a70d1bbe605aa94f066554ffb30bf28580a58769970221de3ba5027f0"

// TestInteropWithReferenceKCP exchanges messages with the upstream C
// implementation over UDP while dropping 15% of datagrams each way. Set
// IKCP_SRC to a directory with ikcp.c and ikcp.h from that commit, and
// CC_BIN to a C compiler.
func TestInteropWithReferenceKCP(t *testing.T) {
	src, cc := os.Getenv("IKCP_SRC"), os.Getenv("CC_BIN")
	if src == "" || cc == "" {
		t.Skip("IKCP_SRC and CC_BIN are not set")
	}
	code, err := os.ReadFile(filepath.Join(src, "ikcp.c"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(code); hex.EncodeToString(sum[:]) != ikcpSHA256 {
		t.Fatalf("ikcp.c is not upstream 2.1.1 (sha256 %x)", sum)
	}
	bin := filepath.Join(t.TempDir(), "echo")
	if out, err := exec.Command(cc, "-O2", "-Wall", "-I", src, "testdata/echo.c", filepath.Join(src, "ikcp.c"), "-o", bin).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	const conv = 0x5eed1234
	cmd := exec.Command(bin, strconv.FormatUint(conv, 10))
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	port, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := net.ResolveUDPAddr("udp", "127.0.0.1:"+strings.TrimSpace(port))
	conn, err := net.DialUDP("udp", nil, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	r := rand.New(rand.NewSource(11))
	var mu sync.Mutex
	k := New(conv, func(b []byte) {
		if r.Float64() >= 0.15 {
			conn.Write(b)
		}
	})
	gameSettings(k)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			mu.Lock()
			if r.Float64() >= 0.15 && !k.Input(buf[:n]) {
				t.Errorf("rejected a datagram from the reference peer")
			}
			mu.Unlock()
		}
	}()
	var sent [][]byte
	got := 0
	start := time.Now()
	deadline := start.Add(60 * time.Second)
	for got < 300 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 300 echoes arrived", got)
		}
		mu.Lock()
		for len(sent) < 300 && k.WaitSnd() < 128 {
			m := message(r, len(sent), 30000)
			k.Send(m)
			sent = append(sent, m)
		}
		for m := k.Recv(); m != nil; m = k.Recv() {
			if !bytes.Equal(m, sent[got]) {
				mu.Unlock()
				t.Fatalf("echo %d differs", got)
			}
			got++
		}
		k.Update(uint32(time.Since(start).Milliseconds()))
		mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Logf("300 messages echoed by upstream ikcp in %v", time.Since(start).Round(time.Millisecond))
}
