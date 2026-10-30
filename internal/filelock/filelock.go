// Package filelock provides advisory lock files that record their owner, so a
// lock left behind by a crashed process can be reclaimed instead of blocking
// every later run.
package filelock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"
)

// StaleAge is how old an unreadable or empty lock must be before it is treated
// as abandoned. Current writers fill the file immediately after creating it,
// so only crashes in that instant or locks from older tool versions qualify.
const StaleAge = 10 * time.Minute

// Owner is the JSON content of a lock file.
type Owner struct {
	PID     int       `json:"pid"`
	Host    string    `json:"host"`
	Started time.Time `json:"started"`
}

func (o Owner) String() string {
	return fmt.Sprintf("pid %d on %s since %s", o.PID, o.Host, o.Started.Format(time.RFC3339))
}

// HeldError reports a lock that belongs to a live or unverifiable owner.
type HeldError struct {
	Path  string
	Owner *Owner // nil when the content is unreadable
}

func (e *HeldError) Error() string {
	if e.Owner == nil {
		return fmt.Sprintf("lock %s is held by an unknown owner (unreadable content, younger than %s); delete it only if no TabForge process is running", e.Path, StaleAge)
	}
	return fmt.Sprintf("lock %s is held by %s; delete it only if that process is no longer running", e.Path, e.Owner)
}

// Lock is an acquired lock file.
type Lock struct{ path string }

// Release removes the lock file.
func (l *Lock) Release() error { return os.Remove(l.path) }

// Acquire creates path exclusively. An existing lock is reclaimed only when it
// was written on this host by a process that no longer exists, or when its
// content is unreadable and older than StaleAge. Otherwise a *HeldError is
// returned. Concurrent reclaimers are serialized by a sibling ".reclaim" file;
// the loser fails instead of deleting the winner's fresh lock.
func Acquire(path string) (*Lock, error) {
	for attempt := 0; attempt < 3; attempt++ {
		err := create(path)
		if err == nil {
			return &Lock{path}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		data, info, err := read(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // released meanwhile
		} else if err != nil {
			return nil, err
		}
		owner, stale := inspect(data, info)
		if !stale {
			return nil, &HeldError{Path: path, Owner: owner}
		}
		if err := reclaim(path, data, info); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("lock %s keeps changing; try again", path)
}

func hostname() string {
	host, _ := os.Hostname()
	return host
}

func create(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(Owner{PID: os.Getpid(), Host: hostname(), Started: time.Now().UTC().Truncate(time.Second)})
	_, err = file.Write(append(data, '\n'))
	// Windows cannot remove a file while a handle is open, so do not keep it.
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

func read(path string) ([]byte, fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("lock %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	return data, info, err
}

// inspect decodes a lock and decides whether its owner is gone.
func inspect(data []byte, info fs.FileInfo) (*Owner, bool) {
	var owner Owner
	if json.Unmarshal(data, &owner) != nil || owner.PID <= 0 || owner.Host == "" {
		return nil, time.Since(info.ModTime()) > StaleAge
	}
	host := hostname()
	if host == "" || !strings.EqualFold(owner.Host, host) {
		return &owner, false // another machine's process cannot be checked
	}
	return &owner, owner.PID != os.Getpid() && !alive(owner.PID)
}

// reclaim deletes the stale lock if it is still exactly the observed file.
func reclaim(path string, observed []byte, observedInfo fs.FileInfo) error {
	guard := path + ".reclaim"
	if err := create(guard); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		data, info, readErr := read(guard)
		if readErr == nil {
			if _, stale := inspect(data, info); stale {
				// A reclaimer crashed mid-way; clear its guard for the next run.
				os.Remove(guard)
				return fmt.Errorf("removed abandoned %s; try again", guard)
			}
		}
		return fmt.Errorf("lock %s is being reclaimed by another process; try again", path)
	}
	defer os.Remove(guard)
	data, info, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if !bytes.Equal(data, observed) || !info.ModTime().Equal(observedInfo.ModTime()) {
		return nil // replaced meanwhile; the caller re-inspects it
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("reclaim stale lock %s: %w", path, err)
	}
	return nil
}
