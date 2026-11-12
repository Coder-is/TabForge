package realtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const realtimeRuntime = "../../examples/realtime/Generated/protocol/runtime.json"

// dotnetOrSkip builds the C# realtime test program once. Set DOTNET_BIN to
// run the Unity SDK core (plain .NET, not the Unity engine) in these tests.
func dotnetOrSkip(t *testing.T) []string {
	t.Helper()
	dotnet := os.Getenv("DOTNET_BIN")
	if dotnet == "" {
		t.Skip("DOTNET_BIN is not set")
	}
	out := t.TempDir()
	if output, err := exec.Command(dotnet, "build", "-nologo", "-v", "q", "-o", out, "../../sdk/unity/Tests/Realtime/RealtimeTests.csproj").CombinedOutput(); err != nil {
		t.Fatalf("dotnet build: %v\n%s", err, output)
	}
	runtime, _ := filepath.Abs(realtimeRuntime)
	return []string{dotnet, filepath.Join(out, "RealtimeTests.dll"), runtime}
}

func TestCSharpRealtimeClient(t *testing.T) {
	cmd := dotnetOrSkip(t)
	run := func(args ...string) *exec.Cmd {
		return exec.Command(cmd[0], append([]string{cmd[1], args[0], cmd[2]}, args[1:]...)...)
	}
	t.Run("units", func(t *testing.T) {
		if out, err := run("unit").CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("codec", func(t *testing.T) {
		checkCodec(t, "C#", func(input, output string) *exec.Cmd { return run("codec", input, output) })
	})
	for _, encoding := range []string{"proto", "json"} {
		t.Run("chaos-"+encoding, func(t *testing.T) {
			out := chaos(t, func(url string) *exec.Cmd { return run("chaos", url, encoding, "2") })
			t.Log(strings.TrimSpace(out))
		})
	}
}

// TestCSharpWebGLBranchCompiles builds the UNITY_WEBGL-only code of the Unity
// transports (the jslib P/Invoke bridge) against stubs of the Unity types it
// uses. It cannot run outside a WebGL player; the jslib itself is exercised
// by TestUnityWebGLBridgeUnderChaos.
func TestCSharpWebGLBranchCompiles(t *testing.T) {
	dotnet := os.Getenv("DOTNET_BIN")
	if dotnet == "" {
		t.Skip("DOTNET_BIN is not set")
	}
	if out, err := exec.Command(dotnet, "build", "-nologo", "-v", "q", "-o", t.TempDir(), "../../sdk/unity/Tests/WebGL/WebGLCompile.csproj").CombinedOutput(); err != nil {
		t.Fatalf("dotnet build: %v\n%s", err, out)
	}
}
