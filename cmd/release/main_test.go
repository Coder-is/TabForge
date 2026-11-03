package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func repository(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFailureNeverPublishesPartialDelivery(t *testing.T) {
	for failAt := 1; failAt <= 4; failAt++ {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "delivery")
			calls := 0
			err := buildUsing(context.Background(), repository(t), options{out: out, target: "darwin-arm64", python: "python"}, func(_ string, _ string, args ...string) error {
				calls++
				if calls == failAt {
					return errors.New("simulated build or installation failure")
				}
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "simulated") {
				t.Fatalf("wrong failure: %v", err)
			}
			if calls != failAt {
				t.Fatalf("continued after failure: %d", calls)
			}
			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("partial delivery or lock left: %v", entries)
			}
		})
	}
}

func TestExistingDeliveryIsPreserved(t *testing.T) {
	out := t.TempDir()
	sentinel := filepath.Join(out, "existing.txt")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	err := buildUsing(context.Background(), repository(t), options{out: out, target: "all"}, func(string, string, ...string) error {
		t.Fatal("started build over existing delivery")
		return nil
	})
	if err == nil {
		t.Fatal("accepted existing delivery")
	}
	raw, _ := os.ReadFile(sentinel)
	if string(raw) != "keep" {
		t.Fatal("modified existing delivery")
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func writeLock(t *testing.T, path string, pid int) {
	t.Helper()
	host, _ := os.Hostname()
	data, _ := json.Marshal(map[string]any{"pid": pid, "host": host, "started": time.Now().UTC()})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// A killed run must not block later releases: its lock is reclaimed and its
// stage directory removed, while other destinations' stages are kept.
func TestKilledRunIsRecovered(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "delivery")
	writeLock(t, out+".lock", deadPID(t))
	stale := filepath.Join(parent, ".delivery.tabforge-release-123")
	other := filepath.Join(parent, ".other.tabforge-release-456")
	for _, dir := range []string{stale, other} {
		if err := os.MkdirAll(filepath.Join(dir, "partial"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	err := buildUsing(context.Background(), repository(t), options{out: out, target: "all", python: "python"}, func(string, string, ...string) error {
		return errors.New("simulated failure")
	})
	if err == nil || !strings.Contains(err.Error(), "simulated") {
		t.Fatalf("stale lock not reclaimed: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(other) {
		t.Fatalf("left behind or removed: %v", entries)
	}
	// A live owner still blocks the destination.
	writeLock(t, out+".lock", os.Getpid())
	err = buildUsing(context.Background(), repository(t), options{out: out, target: "all"}, func(string, string, ...string) error {
		t.Fatal("built while another release holds the lock")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "held by") {
		t.Fatalf("live lock: %v", err)
	}
}

// An interrupt stops before the next step and still cleans up.
func TestInterruptStopsAndCleansUp(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "delivery")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := buildUsing(ctx, repository(t), options{out: out, target: "all", python: "python"}, func(string, string, ...string) error {
		calls++
		cancel() // Ctrl+C arrives while the first step runs.
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("interrupt: %v after %d calls", err, calls)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Fatalf("stage or lock left: %v", entries)
	}
}
