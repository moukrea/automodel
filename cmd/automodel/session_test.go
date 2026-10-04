package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/state"
)

// automodel session: paused work comes back, the work's level is set, by
// ID prefix, under the session's lock.
func TestSessionCmd(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{StateDir: dir, Catalog: filepath.Join("..", "..", "catalog.toml"), CatalogExact: true, NoLastGood: true}
	store := state.Store{Dir: cfg.StateDir}
	now := time.Now()
	store.Update("abc123", func(s *state.Session) bool {
		s.Work = &state.Work{Tier: "low", Goal: "status check", Since: now}
		s.Paused = &state.Work{Tier: "xhigh", Mode: "ultracode", Goal: "the real work", Since: now.Add(-time.Hour)}
		return true
	})
	store.Update("abd456", func(s *state.Session) bool { return true })

	if _, err := findSession(cfg.StateDir, "ab"); err == nil {
		t.Error("an ambiguous prefix resolved")
	}
	if err := sessionCmd(cfg, []string{"--session", "abc", "resume"}); err != nil {
		t.Fatal(err)
	}
	s, _ := store.Load("abc123")
	if w := s.Work; w == nil || w.Tier != "xhigh" || w.Mode != "ultracode" || w.Goal != "the real work" || s.Paused != nil {
		t.Fatalf("after resume: work %+v, paused %+v", s.Work, s.Paused)
	}
	if err := sessionCmd(cfg, []string{"--session", "abc", "resume"}); err == nil {
		t.Error("resumed without paused work")
	}
	if err := sessionCmd(cfg, []string{"--session", "abc123", "work", "high", "--mode", "off"}); err != nil {
		t.Fatal(err)
	}
	s, _ = store.Load("abc123")
	if w := s.Work; w.Tier != "high" || w.Mode != "" || w.Goal != "the real work" {
		t.Fatalf("after work high: %+v", w)
	}
	if err := sessionCmd(cfg, []string{"--session", "abc123", "work", "low", "--mode", "ultracode"}); err == nil {
		t.Error("ultracode set below its minimum tier")
	}
}
