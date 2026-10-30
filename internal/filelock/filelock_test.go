package filelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func writeOwner(t *testing.T, path string, pid int, host string) {
	t.Helper()
	data, _ := json.Marshal(Owner{PID: pid, Host: host, Started: time.Now().UTC()})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireWritesOwnerAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var owner Owner
	host, _ := os.Hostname()
	if json.Unmarshal(data, &owner) != nil || owner.PID != os.Getpid() || owner.Host != host || owner.Started.IsZero() {
		t.Fatalf("owner: %s", data)
	}
	// The same process is alive, so a second acquisition is refused.
	var held *HeldError
	if _, err := Acquire(path); !errors.As(err, &held) || held.Owner == nil || held.Owner.PID != os.Getpid() {
		t.Fatalf("second acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("lock not released")
	}
}

func TestDeadOwnerOnThisHostIsReclaimed(t *testing.T) {
	if !alive(os.Getpid()) {
		t.Fatal("current process reported dead")
	}
	pid := deadPID(t)
	if alive(pid) {
		t.Skipf("pid %d was reused", pid)
	}
	path := filepath.Join(t.TempDir(), "x.lock")
	host, _ := os.Hostname()
	writeOwner(t, path, pid, host)
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), fmt.Sprintf(`"pid":%d`, os.Getpid())) {
		t.Fatalf("lock not rewritten: %s", data)
	}
	if _, err := os.Stat(path + ".reclaim"); !os.IsNotExist(err) {
		t.Fatal("reclaim guard left behind")
	}
}

func TestLiveOrRemoteOwnersAreRefused(t *testing.T) {
	host, _ := os.Hostname()
	for name, owner := range map[string]Owner{
		"live":   {PID: os.Getpid(), Host: host},
		"remote": {PID: deadPID(t), Host: host + "-elsewhere"},
	} {
		path := filepath.Join(t.TempDir(), "x.lock")
		writeOwner(t, path, owner.PID, owner.Host)
		before, _ := os.ReadFile(path)
		_, err := Acquire(path)
		var held *HeldError
		if !errors.As(err, &held) || !strings.Contains(err.Error(), fmt.Sprintf("pid %d on %s", owner.PID, owner.Host)) {
			t.Fatalf("%s: %v", name, err)
		}
		if after, _ := os.ReadFile(path); string(after) != string(before) {
			t.Fatalf("%s: lock modified", name)
		}
	}
}

func TestUnreadableLockReclaimedOnlyWhenOld(t *testing.T) {
	for _, content := range []string{"", "12345\n", "garbage"} {
		path := filepath.Join(t.TempDir(), "x.lock")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		var held *HeldError
		if _, err := Acquire(path); !errors.As(err, &held) || held.Owner != nil {
			t.Fatalf("fresh %q: %v", content, err)
		}
		old := time.Now().Add(-2 * StaleAge)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		lock, err := Acquire(path)
		if err != nil {
			t.Fatalf("old %q: %v", content, err)
		}
		lock.Release()
	}
}

func TestConcurrentReclaimHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	host, _ := os.Hostname()
	writeOwner(t, path, deadPID(t), host)
	// Another reclaimer holds the guard: this one must fail without deleting.
	if err := create(path + ".reclaim"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := Acquire(path); err == nil || !strings.Contains(err.Error(), "being reclaimed") {
		t.Fatalf("guarded reclaim: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("loser modified the lock")
	}
	os.Remove(path + ".reclaim")
	const n = 8
	results := make(chan error, n)
	for range n {
		go func() {
			lock, err := Acquire(path)
			if err == nil {
				// Hold briefly so the other goroutines observe the live owner.
				time.Sleep(50 * time.Millisecond)
				lock.Release()
			}
			results <- err
		}()
	}
	wins := 0
	for range n {
		if <-results == nil {
			wins++
		}
	}
	if wins == 0 {
		t.Fatal("no goroutine acquired the stale lock")
	}
}
