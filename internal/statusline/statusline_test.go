package statusline

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		{nil, "automodel: Opus 5.5 high (default)"},
		{&state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh", Trigger: "initial", Why: "new", Confidence: 0.82, DecidedAt: now}, "automodel: Opus 5.5 xhigh (new 0.82)"},
		{&state.Decision{Tier: "high", Mode: "ultracode", Model: "claude-opus-5-5", Effort: "xhigh", Workflows: true, Trigger: "compact", Confidence: 0.74, DecidedAt: now}, "automodel: Opus 5.5 xhigh +ultracode (0.74) ↻ compact"},
		{&state.Decision{Tier: "low", Model: "claude-opus-5-5", Effort: "low", Trigger: "warm", Confidence: 0.91, DecidedAt: now}, "automodel: Opus 5.5 low (0.91) ↻ switched"},
		{&state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high", Trigger: "cold", Confidence: 0.7, DecidedAt: now.Add(-time.Minute)}, "automodel: Opus 5.5 high (0.70)"},
		{&state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high", Trigger: "fallback", Cause: "cold", DecidedAt: now}, "automodel: Opus 5.5 high (⚠ fallback) ↻ cold"},
	}
	for _, tc := range cases {
		if got := Render(env, &state.Session{Main: tc.d}, now); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
	var out strings.Builder
	in := `{"session_id":"s","model":{"id":"jev","display_name":"Jev (auto)"}}`
	if err := Run(env, strings.NewReader(in), &out); err != nil || !strings.Contains(out.String(), "automodel: Opus 5.5 high") {
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
	if got := Render(env, sess, time.Now()); !strings.Contains(got, "high (pinned)") {
		t.Fatalf("render = %q", got)
	}
	sess.Pin, sess.JevIssue = "", "no OpenRouter key"
	if got := Render(env, sess, time.Now()); !strings.Contains(got, "⚠ jev: no OpenRouter key") {
		t.Fatalf("render = %q", got)
	}
}

func jsonEnv(t *testing.T) *router.Env {
	t.Helper()
	c, _, err := catalog.Load("../../catalog.toml", time.Now(), 3650)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.StateDir = t.TempDir()
	return &router.Env{Cfg: cfg, Catalog: c, State: state.Store{Dir: cfg.StateDir}, Now: time.Now}
}

func runJSON(t *testing.T, env *router.Env, in string) map[string]any {
	t.Helper()
	var out strings.Builder
	if err := RunJSON(env, strings.NewReader(in), &out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "\n") {
		t.Fatalf("not one line: %q", out.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out.String()), &m); err != nil {
		t.Fatalf("%q: %v", out.String(), err)
	}
	m["_raw"] = strings.TrimSpace(out.String())
	return m
}

const jevIn = `{"session_id":"s","model":{"id":"jev","display_name":"Jev (auto)"}}`

func TestJSONNotRouted(t *testing.T) {
	env := jsonEnv(t)
	for _, in := range []string{`{}`, `garbage`, `{"session_id":"s","model":{"id":"claude-opus-5-5","display_name":"Opus"}}`, `{"model":{"id":"jev"}}`} {
		if m := runJSON(t, env, in); m["_raw"] != `{"v":1,"routed":false}` {
			t.Errorf("%s: %s", in, m["_raw"])
		}
	}
}

func TestJSONRouted(t *testing.T) {
	env := jsonEnv(t)
	now := time.Now()
	env.State.Update("s", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "high", Mode: "ultracode", Model: "claude-opus-5-5", Effort: "xhigh", Trigger: "initial", Confidence: 0.86, DecidedAt: now.Add(-time.Hour)}
		return true
	})
	m := runJSON(t, env, jevIn)
	want := `{"v":1,"routed":true,"alias":"jev","model":"claude-opus-5-5","label":"Opus 5.5","effort":"xhigh","mode":"ultracode","state":"routed","confidence":0.86,"pin":"","issue":"","flash":"","budget":"","why":"","why_p":0,"from":"","claude_effort":"","text":"automodel: Opus 5.5 xhigh +ultracode (0.86)"}`
	if m["_raw"] != want {
		t.Errorf("got  %s\nwant %s", m["_raw"], want)
	}
	// Same side effect as the text statusline: the model switch is recorded.
	if s, _ := env.State.Load("s"); s.Model != "jev" || s.ModelSource != "statusline" {
		t.Errorf("model not recorded: %+v", s)
	}
	// The text field is what the text statusline prints.
	var out strings.Builder
	Run(env, strings.NewReader(jevIn), &out)
	if strings.TrimSpace(out.String()) != m["text"] {
		t.Errorf("text %q vs statusline %q", m["text"], out.String())
	}
}

func TestJSONStates(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		sess state.Session
		want map[string]any
	}{
		{"default", state.Session{}, map[string]any{"state": "default", "confidence": 0.0, "effort": "high", "flash": "", "text": "automodel: Opus 5.5 high (default)"}},
		{"pinned", state.Session{Pin: "high", Main: &state.Decision{Model: "claude-opus-5-5", Effort: "high", Trigger: "pinned", Confidence: 1, DecidedAt: now}},
			map[string]any{"state": "pinned", "pin": "high", "confidence": 0.0, "text": "automodel: Opus 5.5 high (pinned)"}},
		{"fallback+issue+flash", state.Session{JevIssue: "no OpenRouter key", Main: &state.Decision{Model: "claude-opus-5-5", Effort: "high", Trigger: "fallback", Cause: "cold", DecidedAt: now}},
			map[string]any{"state": "fallback", "issue": "no OpenRouter key", "flash": "cold", "confidence": 0.0, "text": "automodel: Opus 5.5 high (⚠ fallback) ⚠ jev: no OpenRouter key ↻ cold"}},
		{"switched", state.Session{Main: &state.Decision{Model: "claude-opus-5-5", Effort: "low", Trigger: "warm", Confidence: 0.91, DecidedAt: now}},
			map[string]any{"state": "routed", "flash": "switched", "confidence": 0.91, "text": "automodel: Opus 5.5 low (0.91) ↻ switched"}},
		{"compact", state.Session{Main: &state.Decision{Model: "claude-opus-5-5", Effort: "xhigh", Trigger: "compact", Confidence: 0.7, DecidedAt: now}},
			map[string]any{"flash": "compact"}},
		{"old flash", state.Session{Main: &state.Decision{Model: "claude-opus-5-5", Effort: "xhigh", Trigger: "compact", Confidence: 0.7, DecidedAt: now.Add(-time.Hour)}},
			map[string]any{"flash": ""}},
		{"over budget", state.Session{TotalUSD: 2, Main: &state.Decision{Model: "claude-opus-5-5", Effort: "low", Trigger: "cold", Confidence: 0.8, DecidedAt: now.Add(-time.Hour)}},
			map[string]any{"budget": "over", "text": "automodel: Opus 5.5 low (0.80) ⚠ budget"}},
	}
	for _, tc := range cases {
		env := jsonEnv(t)
		if tc.name == "over budget" {
			env.Cfg.Budget.USDPerSession = 1
		}
		sess := tc.sess
		env.State.Update("s", func(s *state.Session) bool { *s = sess; s.SessionID = "s"; return true })
		m := runJSON(t, env, jevIn)
		for _, k := range []string{"v", "routed", "alias", "model", "label", "effort", "mode", "state", "confidence", "pin", "issue", "flash", "budget", "text"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: no %q in %s", tc.name, k, m["_raw"])
			}
		}
		for k, want := range tc.want {
			if m[k] != want {
				t.Errorf("%s: %s = %v, want %v (%s)", tc.name, k, m[k], want, m["_raw"])
			}
		}
	}
}

func TestJSONCatalogError(t *testing.T) {
	var out strings.Builder
	CatalogErrorJSON("jev", strings.NewReader(jevIn), &out)
	want := `{"v":1,"routed":true,"alias":"jev","model":"","label":"","effort":"","mode":"","state":"error","confidence":0,"pin":"","issue":"catalog","flash":"","budget":"","why":"","why_p":0,"from":"","claude_effort":"","text":"automodel: ⚠ catalog"}` + "\n"
	if out.String() != want {
		t.Errorf("got  %s\nwant %s", out.String(), want)
	}
	out.Reset()
	CatalogErrorJSON("jev", strings.NewReader(`{"session_id":"s","model":{"id":"opus"}}`), &out)
	if !strings.HasPrefix(out.String(), `{"v":1,"routed":false,`) {
		t.Errorf("not routed: %s", out.String())
	}
}

// --json never runs the chained statusline (it may be agentline itself).
func TestJSONNeverChains(t *testing.T) {
	env := jsonEnv(t)
	mark := filepath.Join(t.TempDir(), "ran")
	env.Cfg.StatuslineCommand = "touch " + mark
	runJSON(t, env, jevIn)
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("--json ran statusline_command")
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.StateDir, "statusline")); err == nil {
		t.Fatal("--json touched the chain cache")
	}
}

// The chained command knows it runs under automodel's statusline.
func TestChainedEnv(t *testing.T) {
	t.Setenv(ChainedEnv, "0") // replaced, not duplicated
	if got := chain(t.TempDir(), chainedEnvEcho, "s", []byte(`{}`)); got != "chained=1" {
		t.Fatalf("chained env: %q", got)
	}
}

func TestRealEffortVsClaudeCode(t *testing.T) {
	c, _, _ := catalog.Load("../../catalog.toml", time.Now(), 3650)
	env := &router.Env{Cfg: config.Default(), Catalog: c, Now: time.Now}
	sess := &state.Session{ClientEffort0: "xhigh", Main: &state.Decision{Tier: "low", Model: "claude-opus-5-5", Effort: "low", Trigger: "warm", Confidence: 0.95}}
	v := Build(env, sess, time.Now().Add(time.Hour))
	if v.ClaudeEffort != "xhigh" || v.Text != "automodel: Opus 5.5 low "+strike("xhigh")+" (0.95)" {
		t.Fatalf("got %q (claude_effort %q)", v.Text, v.ClaudeEffort)
	}
	sess.Main.Effort, sess.Main.Tier = "xhigh", "xhigh"
	if v := Build(env, sess, time.Now().Add(time.Hour)); v.ClaudeEffort != "" || strings.Contains(v.Text, strike("xhigh")) {
		t.Fatalf("same effort still annotated: %q", v.Text)
	}
}

func TestJSONReadOnlyWritesNothing(t *testing.T) {
	env := jsonEnv(t)
	var out strings.Builder
	if err := RunJSON(env, strings.NewReader(jevIn), &out, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"routed":true`) {
		t.Errorf("read-only view: %s", out.String())
	}
	files, _ := filepath.Glob(filepath.Join(env.Cfg.StateDir, "sessions", "*"))
	if len(files) != 0 {
		t.Errorf("read-only wrote %v", files)
	}
	runJSON(t, env, jevIn) // the statusline itself records the model
	if s, _ := env.State.Load("s"); s.Model != "jev" || s.ModelSource != "statusline" {
		t.Errorf("recorded %q from %q", s.Model, s.ModelSource)
	}
}

// The last decision in short: the effort it left, why, Jev's confidence;
// the effort Claude Code still shows struck through (red in its own status
// line, plain in the JSON text other status lines embed).
func TestRenderWhy(t *testing.T) {
	v := View{Label: "Opus 5.5", Effort: "high", State: "routed", Confidence: 0.44, Why: "inform", WhyP: 0.59, From: "xhigh", ClaudeEffort: "xhigh"}
	if got, want := v.text(), "automodel: Opus 5.5 high x\u0336h\u0336i\u0336g\u0336h\u0336 (xhigh→high · inform 0.59)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	v = View{Label: "Opus 5.5", Effort: "xhigh", State: "routed", Why: "go-ahead"}
	if got := v.text(); got != "automodel: Opus 5.5 xhigh (go-ahead)" {
		t.Errorf("kept: %q", got)
	}
}
