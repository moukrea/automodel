package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store hot-reloads a catalog when its mtime changes. An invalid catalog never
// replaces a valid one: the proxy keeps the previous one in memory, and
// one-shot processes (hooks) fall back to the last good copy on disk.
type Store struct {
	Path      string
	LastGood  string // copy of the last valid catalog; empty disables it
	StaleDays int

	mu      sync.Mutex
	cur     *Catalog
	mtime   time.Time
	size    int64
	checked time.Time
}

// MinCheckInterval bounds how often Get stats the file.
const MinCheckInterval = time.Second

// Get returns the current valid catalog, reloading it if the file changed.
func (s *Store) Get() (*Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.cur != nil && now.Sub(s.checked) < MinCheckInterval {
		return s.cur, nil
	}
	s.checked = now
	st, err := os.Stat(s.Path)
	if err != nil {
		if s.cur != nil {
			return s.cur, nil
		}
		return s.fallback(err)
	}
	if s.cur != nil && st.ModTime().Equal(s.mtime) && st.Size() == s.size {
		return s.cur, nil
	}
	s.mtime, s.size = st.ModTime(), st.Size()
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return s.keep(err)
	}
	c, err := Parse(data)
	if err != nil {
		return s.keep(err)
	}
	if errs := c.Validate(now, s.StaleDays).Errors(); len(errs) > 0 {
		return s.keep(fmt.Errorf("invalid catalog: %v", errs))
	}
	s.cur = c
	s.saveLastGood(data)
	return c, nil
}

func (s *Store) keep(err error) (*Catalog, error) {
	log.Printf("catalog %s: %v (keeping previous)", s.Path, err)
	if s.cur != nil {
		return s.cur, nil
	}
	return s.fallback(err)
}

func (s *Store) fallback(cause error) (*Catalog, error) {
	if s.LastGood == "" {
		return nil, cause
	}
	data, err := os.ReadFile(s.LastGood)
	if err != nil {
		return nil, cause
	}
	c, err := Parse(data)
	if err != nil {
		return nil, cause
	}
	s.cur = c
	return c, nil
}

func (s *Store) saveLastGood(data []byte) {
	if s.LastGood == "" {
		return
	}
	if old, err := os.ReadFile(s.LastGood); err == nil && string(old) == string(data) {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.LastGood), 0o755)
	tmp := s.LastGood + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, s.LastGood)
	}
}
