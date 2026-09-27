package flock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Lockers on the same file through separate handles (as separate processes
// would) never overlap, and the lock blocks neither appends nor reads.
func TestLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	var mu sync.Mutex
	inside, overlap := 0, false
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					t.Error(err)
					return
				}
				unlock, err := Lock(f)
				if err != nil {
					f.Close()
					t.Error(err)
					return
				}
				mu.Lock()
				inside++
				overlap = overlap || inside > 1
				mu.Unlock()
				if _, err := f.Write([]byte("x\n")); err != nil {
					t.Error(err)
				}
				if _, err := os.ReadFile(path); err != nil {
					t.Error(err)
				}
				time.Sleep(time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				unlock()
				f.Close()
			}
		}()
	}
	wg.Wait()
	if overlap {
		t.Error("two holders at once")
	}
	if b, _ := os.ReadFile(path); len(b) != 4*10*2 {
		t.Errorf("%d bytes written, want %d", len(b), 4*10*2)
	}
}
