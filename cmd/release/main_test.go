package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			err := buildUsing(repository(t), options{out: out, target: "darwin-arm64", python: "python"}, func(_ string, _ string, args ...string) error {
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
	err := buildUsing(repository(t), options{out: out, target: "all"}, func(string, string, ...string) error {
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
