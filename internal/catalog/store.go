package catalog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store gives the catalog to route with. With the default tuning it is
// the catalog shipped in the binary (Default, else Shipped). With a custom
// tuning it is the file at Path layered over the default (or the file
// alone with Exact), hot-reloaded when its mtime changes. An invalid
// catalog never replaces a valid one: the proxy keeps the previous one in
// memory, and one-shot processes (hooks) fall back to the last good copy
// on disk, then to the default.
type Store struct {
	Path      string
	Custom    bool   // route with the file at Path (else the default)
	Exact     bool   // the file is a whole catalog: no layering over the default
	Default   []byte // the default catalog (nil: Shipped)
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
	if !s.Custom && !s.Exact && s.defaultData() != nil {
		if s.cur == nil {
			c, err := s.parse(s.defaultData(), now)
			if err != nil {
				return nil, fmt.Errorf("default catalog: %w", err)
			}
			s.cur = c
		}
		return s.cur, nil
	}
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
	// A partial file is layered over the default; a whole catalog (it
	// sets meta.schema) is used as it is.
	if !s.Exact && s.defaultData() != nil && !IsWhole(data) {
		if data, err = Merge(s.defaultData(), data); err != nil {
			return s.keep(err)
		}
	}
	c, err := s.parse(data, now)
	if err != nil {
		return s.keep(err)
	}
	s.cur = c
	s.saveLastGood(data)
	return c, nil
}

func (s *Store) defaultData() []byte {
	if s.Default != nil {
		return s.Default
	}
	return Shipped
}

func (s *Store) parse(data []byte, now time.Time) (*Catalog, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if errs := c.Validate(now, s.StaleDays).Errors(); len(errs) > 0 {
		return nil, fmt.Errorf("invalid catalog: %v", errs)
	}
	return c, nil
}

// Source describes where the catalog comes from, for doctor and logs.
func (s *Store) Source() string {
	switch {
	case s.Exact:
		return s.Path
	case !s.Custom && s.defaultData() != nil:
		return "default"
	case s.defaultData() == nil:
		return s.Path
	default:
		if data, err := os.ReadFile(s.Path); err == nil && IsWhole(data) {
			return "custom: " + s.Path
		}
		return "custom: " + s.Path + " over the default"
	}
}

func (s *Store) keep(err error) (*Catalog, error) {
	log.Printf("catalog %s: %v (keeping previous)", s.Path, err)
	if s.cur != nil {
		return s.cur, nil
	}
	return s.fallback(err)
}

func (s *Store) fallback(cause error) (*Catalog, error) {
	if s.LastGood != "" {
		if data, err := os.ReadFile(s.LastGood); err == nil {
			if c, err := Parse(data); err == nil {
				s.cur = c
				return c, nil
			}
		}
	}
	// A broken or missing custom file never stops routing: the default does.
	if d := s.defaultData(); d != nil && !s.Exact {
		if c, err := s.parse(d, time.Now()); err == nil {
			log.Printf("catalog: custom tuning unusable (%v): routing with the default", cause)
			s.cur = c
			return c, nil
		}
	}
	return nil, cause
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
