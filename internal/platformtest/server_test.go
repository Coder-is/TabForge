package platformtest

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Coder-is/TabForge/protocol"
)

func fixture(t *testing.T) *Server {
	t.Helper()
	c, err := protocol.Load("../../examples/protocol/contract.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPlatformTransportsAgainstGo(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js 24+ required")
	}
	version, err := exec.Command(node, "--version").Output()
	if err != nil {
		t.Fatal(err)
	}
	major, _ := strconv.Atoi(strings.Split(strings.TrimPrefix(string(version), "v"), ".")[0])
	if major < 24 {
		t.Skip("Node.js 24+ required for TypeScript")
	}
	fixture := fixture(t)
	server := httptest.NewServer(fixture)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, node, "../../examples/platforms/integration.mjs", server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("platform integration: %v\n%s", err, output)
	}
	until := time.Now().Add(time.Second)
	for fixture.Cancelled.Load() < 6 && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if fixture.Cancelled.Load() < 6 {
		t.Fatalf("cancel/break/timeout did not disconnect all six HTTP requests: %d", fixture.Cancelled.Load())
	}
}
func TestCSharpCoreAgainstGo(t *testing.T) {
	dotnet := os.Getenv("DOTNET_BIN")
	if dotnet == "" {
		t.Skip("set DOTNET_BIN for C# core HTTP integration; this is not a Unity engine test")
	}
	server := httptest.NewServer(fixture(t))
	defer server.Close()
	runtime, _ := filepath.Abs("../../examples/protocol/generated/runtime.json")
	project, _ := filepath.Abs("../../sdk/unity/Tests/CoreTests.csproj")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, dotnet, "run", "--project", project, "--", runtime, server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("C# core integration: %v\n%s", err, output)
	}
	t.Log(string(output))
}
