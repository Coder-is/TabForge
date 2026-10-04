package helper

import (
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileLoaderBoundsAndDeduplicatesLoads(t *testing.T) {
	loader := NewFileLoader(false, "")
	const count, workers = 12, 3
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("file-%d.csv", i)
		loader.AddFile(name)
		loader.AddFile("nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + name)
	}
	var active, peak int32
	var mu sync.Mutex
	calls := make(map[string]int)
	started, release, done := make(chan string, count*2), make(chan struct{}), make(chan struct{})
	go func() {
		loader.commit(workers, func(filename, cacheDir string) (TableFile, error) {
			n := atomic.AddInt32(&active, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
					break
				}
			}
			mu.Lock()
			calls[filename]++
			mu.Unlock()
			started <- filename
			<-release
			atomic.AddInt32(&active, -1)
			return NewCSVFile(), nil
		})
		close(done)
	}()
	for i := 0; i < workers; i++ {
		<-started
	}
	select {
	case name := <-started:
		t.Errorf("exceeded worker limit with %s", name)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("loader did not finish")
	}
	if peak != workers || len(calls) != count {
		t.Fatalf("peak=%d, unique loads=%d", peak, len(calls))
	}
	for name, n := range calls {
		if n != 1 {
			t.Errorf("%s loaded %d times", name, n)
		}
		if _, err := loader.GetFile("nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + name); err != nil {
			t.Errorf("normalized lookup failed: %v", err)
		}
	}
	loader.commit(workers, func(string, string) (TableFile, error) {
		t.Error("commit reloaded already processed files")
		return nil, errors.New("unexpected load")
	})
}

func TestFileLoaderReturnsInputErrors(t *testing.T) {
	for _, syncLoad := range []bool{true, false} {
		t.Run(fmt.Sprintf("sync=%v", syncLoad), func(t *testing.T) {
			loader := NewFileLoader(syncLoad, "")
			loader.AddFile("unsupported.invalid")
			if !syncLoad {
				loader.Commit()
			}
			_, err := loader.GetFile("unsupported.invalid")
			if err == nil || !strings.Contains(err.Error(), "UnknownInputFileExtension") {
				t.Fatalf("expected structured input error: %v", err)
			}
		})
	}
}

func TestFileLoaderReadsCSV(t *testing.T) {
	dir, err := ioutil.TempDir("", "tabtoy-loader-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "data.csv")
	if err := ioutil.WriteFile(name, []byte("ID,Name\n1,hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, syncLoad := range []bool{true, false} {
		loader := NewFileLoader(syncLoad, "")
		loader.AddFile(name)
		if !syncLoad {
			loader.Commit()
		}
		file, err := loader.GetFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if value := file.Sheets()[0].GetValue(1, 1, nil); value != "hello" {
			t.Fatalf("unexpected cell: %q", value)
		}
	}
}
