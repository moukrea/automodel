package statusline

import (
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

func TestRender(t *testing.T) {
	c, _, _ := catalog.Load("../../catalog.toml", time.Now(), 3650)
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	env := &router.Env{Cfg: cfg, Catalog: c, State: state.Store{Dir: cfg.StateDir}, Now: time.Now}
	now := time.Now()
	cases := []struct {
		d    *state.Decision
		want string
	}{
		{nil, "jev → opus-5.5·high (default)"},
		{&state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh", Trigger: "initial", Confidence: 0.82, DecidedAt: now}, "jev → opus-5.5·xhigh 0.82"},
		{&state.Decision{Tier: "high", Mode: "ultracode", Model: "claude-opus-5-5", Effort: "xhigh", Workflows: true, Trigger: "compact", Confidence: 0.74, DecidedAt: now}, "jev → opus-5.5·xhigh +ultracode 0.74 ↻ compact"},
		{&state.Decision{Tier: "low", Model: "claude-opus-5-5", Effort: "low", Trigger: "warm", Confidence: 0.91, DecidedAt: now}, "jev → opus-5.5·low 0.91 ↻ switched"},
		{&state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high", Trigger: "cold", Confidence: 0.7, DecidedAt: now.Add(-time.Minute)}, "jev → opus-5.5·high 0.70"},
		{&state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high", Trigger: "fallback", Cause: "cold", DecidedAt: now}, "jev → opus-5.5·high ⚠ fallback ↻ cold"},
	}
	for _, tc := range cases {
		if got := Render(env, &state.Session{Main: tc.d}, now); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	var out strings.Builder
	in := `{"session_id":"s","model":{"id":"jev","display_name":"Jev (auto)"}}`
	if err := Run(env, strings.NewReader(in), &out); err != nil || !strings.Contains(out.String(), "jev → opus-5.5·high") {
		t.Errorf("Run = %q %v", out.String(), err)
	}
	if s, _ := env.State.Load("s"); s.Model != "jev" || s.ModelSource != "statusline" {
		t.Errorf("model not recorded: %+v", s)
	}
}

// The user's statusline runs detached behind a cache: the first run waits
// for it, later runs print the cache without waiting.
func TestChainCached(t *testing.T) {
	dir := t.TempDir()
	cmd := "sleep 0.3; echo mine"
	if got := chain(dir, cmd, "s1", []byte(`{}`)); got != "mine" {
		t.Fatalf("first run = %q", got)
	}
	start := time.Now()
	if got := chain(dir, cmd, "s1", []byte(`{}`)); got != "mine" || time.Since(start) > 150*time.Millisecond {
		t.Errorf("cached run = %q in %v", got, time.Since(start))
	}
}

func TestRenderPinned(t *testing.T) {
	c, _, _ := catalog.Load("../../catalog.toml", time.Now(), 3650)
	env := &router.Env{Cfg: config.Default(), Catalog: c, Now: time.Now}
	sess := &state.Session{Pin: "high", Main: &state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high", Trigger: "pinned", Confidence: 1}}
	if got := Render(env, sess, time.Now()); !strings.Contains(got, "·high (pinned)") {
		t.Fatalf("render = %q", got)
	}
}
