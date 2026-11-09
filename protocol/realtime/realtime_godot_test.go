package realtime

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGodotRealtimeClient runs the GDScript client (Godot headless). Set
// GODOT_BIN to a Godot 4.5.1+ binary.
func TestGodotRealtimeClient(t *testing.T) {
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
	t.Run("units", func(t *testing.T) {
		out, err := run("unit").CombinedOutput()
		passed(t, out, err, "unit")
	})
	t.Run("codec", func(t *testing.T) {
		checkCodec(t, "GDScript", func(input, output string) *exec.Cmd { return run("codec", input, output) })
	})
	for _, encoding := range []string{"proto", "json"} {
		t.Run("chaos-"+encoding, func(t *testing.T) {
			var cmd *exec.Cmd
			out := chaos(t, func(url string) *exec.Cmd { cmd = run("chaos", url, encoding, "2"); return cmd })
			if !strings.Contains(out, "realtime chaos: 0 failures") || strings.Contains(out, "SCRIPT ERROR") {
				t.Fatalf("Godot chaos:\n%s", out)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, "{") {
					t.Log(line)
				}
			}
		})
	}
}
