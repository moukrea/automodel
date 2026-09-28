package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/jev"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

// fa is a scripted Jev reading: the tier Jev leans to, its confidence,
// the ultracode yes-probability and the continuation yes-probability.
type fa struct {
	tier  string
	conf  float64
	ultra float64
	cont  float64
	inf   float64
	asked float64 // asked tiers (tier_*)
}

func answer(tier string, conf float64) fa { return fa{tier: tier, conf: conf, ultra: 0.05, cont: 0.1} }

// fakeJev answers every request with the next queued reading (the last one
// repeats), shaped like Jev's Score and Noul answers.
type fakeJev struct {
	mu       sync.Mutex
	cat      *catalog.Catalog
	answers  []fa
	requests []jev.Request
	fail     bool
	delay    time.Duration // before answering (outside the lock)
}

// No detached processes from tests.
func init() {
	spawnLate = func(*Input, string, time.Time) {}
	spawnCompact = func(*Input, time.Time) {}
	compactWait = 300 * time.Millisecond
}

func (f *fakeJev) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	d := f.delay
	f.mu.Unlock()
	time.Sleep(d)
	f.mu.Lock()
	defer f.mu.Unlock()
	var req jev.Request
	json.NewDecoder(r.Body).Decode(&req)
	f.requests = append(f.requests, req)
	if f.fail || len(f.answers) == 0 {
		w.WriteHeader(502)
		io.WriteString(w, `{"error":{"code":502,"message":"upstream down"}}`)
		return
	}
	a := f.answers[0]
	if len(f.answers) > 1 {
		f.answers = f.answers[1:]
	}
	answers := map[string]any{}
	for id, q := range req.Questions {
		switch q.Type {
		case "score":
			var ids []string
			for _, sc := range catalog.Scopes {
				if t := f.cat.Tier(sc, a.tier); t != nil && !t.Asked() && ids == nil {
					for _, t := range f.cat.ScoredTiers(sc) {
						ids = append(ids, t.ID)
					}
				}
			}
			n := float64(len(ids))
			peak := (a.conf*(n-1) + 1) / n // Jev: confidence = (n·peak - 1)/(n - 1)
			probs := map[string]float64{}
			for i, t := range ids {
				switch {
				case t == a.tier:
					probs[fmt.Sprint(i)] = peak
				case i > 0 && ids[i-1] == a.tier, i == len(ids)-1 && ids[i-1] != a.tier && a.tier == ids[len(ids)-1]:
					probs[fmt.Sprint(i)] = 1 - peak
				default:
					probs[fmt.Sprint(i)] = 0
				}
			}
			if a.tier == ids[len(ids)-1] {
				probs[fmt.Sprint(len(ids)-2)] = 1 - peak
			}
			answers[id] = map[string]any{"type": "score", "probabilities": probs, "confidence": a.conf}
		case "noul":
			v := a.ultra
			if id == jev.QContinues {
				v = a.cont
			}
			if id == jev.QInforms {
				v = a.inf
			}
			if strings.HasPrefix(id, jev.QTierPfx) {
				v = a.asked
			}
			answers[id] = map[string]any{"type": "noul", "noul": v}
		case "choice":
			answers[id] = map[string]any{"type": "choice", "choice": a.tier, "confidence": a.conf, "probabilities": map[string]float64{a.tier: a.conf}}
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"id": "d1", "model": req.Model, "answers": answers,
		"usage": map[string]any{"input_tokens": 1000, "output_tokens": 0, "cost": 0.000042},
	})
}

func (f *fakeJev) last() jev.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeJev) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func setup(t *testing.T, fj *fakeJev) *router.Env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("CLAUDE_PID", "")
	t.Setenv("CLAUDE_PROJECT_DIR", home)
	srv := httptest.NewServer(fj)
	t.Cleanup(srv.Close)
	cfg := config.Default()
	cfg.StateDir = filepath.Join(home, "state")
	cfg.Ledger = filepath.Join(home, "state", "ledger.jsonl")
	cfg.Catalog = "../../catalog.toml"
	// A live listener stands in for the proxy the hooks check first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cfg.Listen = ln.Addr().String()
	t.Setenv("ANTHROPIC_BASE_URL", "")
	c, is, err := catalog.Load(cfg.Catalog, time.Now(), 3650)
	if err != nil || len(is.Errors()) > 0 {
		t.Fatal(err, is)
	}
	fj.cat = c
	return &router.Env{
		Cfg: cfg, Catalog: c, State: state.Store{Dir: cfg.StateDir}, Ledger: ledger.Ledger{Path: cfg.Ledger},
		Jev: &jev.Client{URL: srv.URL, APIKey: "k"}, Now: time.Now,
	}
}

func run(t *testing.T, env *router.Env, name string, in map[string]any) *Output {
	t.Helper()
	b, _ := json.Marshal(in)
	var out bytes.Buffer
	if err := Run(name, env, bytes.NewReader(b), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return nil
	}
	var o Output
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("bad output %q: %v", out.String(), err)
	}
	return &o
}

func markJev(t *testing.T, env *router.Env, sid string) {
	env.State.Update(sid, func(s *state.Session) bool { s.Model, s.ModelSource = "jev", "proxy"; return true })
}

func TestDecideLifecycle(t *testing.T) {
	ultra := fa{tier: "xhigh", conf: 0.9, ultra: 0.9, cont: 0.1}
	fj := &fakeJev{answers: []fa{ultra}}
	env := setup(t, fj)
	sid := "s1"
	markJev(t, env, sid)
	cwd := t.TempDir()
	tp := filepath.Join(cwd, "t.jsonl")
	prompt := func(p string) *Output {
		return run(t, env, "decide", map[string]any{"session_id": sid, "prompt": p, "cwd": cwd, "transcript_path": tp})
	}

	// initial → xhigh with the ultracode mode, opt-in injected once.
	out := prompt("Audit the whole codebase for injection bugs")
	if out == nil || !strings.Contains(out.HookSpecificOutput.AdditionalContext, "Ultracode is on") {
		t.Fatalf("expected ultracode notice, got %+v", out)
	}
	if st := fj.last().State.(map[string]any); st["phase"] != "initial" || !strings.Contains(st["task"].(string), "Audit") {
		t.Errorf("state = %v", st)
	}
	if q := fj.last().Questions; q[jev.QLevel].Type != "score" || q[jev.QModePfx+"ultracode"].Type != "noul" || q[jev.QContinues].Type != "" || q[jev.QInforms].Type != "" {
		t.Errorf("initial questions = %+v", q)
	}
	sess, _ := env.State.Load(sid)
	if sess.Main.Tier != "xhigh" || sess.Main.Mode != "ultracode" || sess.Main.Effort != "xhigh" || !sess.Main.Workflows || sess.Main.Epoch != 1 {
		t.Fatalf("main = %+v", sess.Main)
	}

	// warm "continue": Jev is asked (with the continuation question), the
	// tier is kept, no second notice.
	fj.answers = []fa{{tier: "xhigh", conf: 0.9, ultra: 0.5, cont: 0.9}}
	if out := prompt("continue with the remaining modules"); out != nil {
		t.Errorf("unexpected output %+v", out)
	}
	if fj.calls() != 2 || fj.last().Questions[jev.QContinues].Type != "noul" || fj.last().State.(map[string]any)["phase"] != "warm" {
		t.Errorf("warm call: %d calls, %+v", fj.calls(), fj.last())
	}
	if sess, _ = env.State.Load(sid); sess.Main.Epoch != 1 || sess.Main.Mode != "ultracode" {
		t.Errorf("warm continue changed the decision: %+v", sess.Main)
	}

	// compaction → redecide on the summary; leaving ultracode says so.
	os.WriteFile(tp, []byte(
		`{"type":"system","subtype":"compact_boundary"}`+"\n"+
			`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"Summary: fixed three SQL injections"}}`+"\n"), 0o644)
	run(t, env, "precompact", map[string]any{"session_id": sid, "trigger": "auto"})
	fj.answers = []fa{answer("high", 0.9)}
	out = prompt("now write the changelog")
	if out == nil || !strings.Contains(out.HookSpecificOutput.AdditionalContext, "Ultracode is now off") {
		t.Fatalf("expected off notice, got %+v", out)
	}
	st := fj.last().State.(map[string]any)
	if st["phase"] != "post_compact" || !strings.Contains(st["compaction_summary"].(string), "fixed three SQL injections") ||
		st["current"].(map[string]any)["tier"] != "xhigh" {
		t.Errorf("compact state = %v", st)
	}
	sess, _ = env.State.Load(sid)
	if sess.Main.Tier != "high" || sess.Main.Mode != "" || sess.Main.Trigger != "compact" || sess.CompactPending || sess.Main.Epoch != 2 {
		t.Errorf("after compact: %+v", sess)
	}

	// cold: last activity older than the cache TTL.
	env.State.Update(sid, func(s *state.Session) bool {
		s.LastPromptAt, s.LastAPIAt = time.Now().Add(-2*time.Hour), time.Time{}
		return true
	})
	fj.answers = []fa{answer("medium", 0.9)}
	prompt("small follow-up")
	sess, _ = env.State.Load(sid)
	if sess.Main.Tier != "medium" || sess.Main.Trigger != "cold" {
		t.Errorf("cold: %+v", sess.Main)
	}
	if st := fj.last().State.(map[string]any); st["phase"] != "resumed" {
		t.Errorf("cold phase = %v", st["phase"])
	}

	// subagent hand-backs never trigger a decision.
	n := fj.calls()
	prompt("<agent-message from=\"a1\">\n report\n</agent-message>")
	if fj.calls() != n {
		t.Error("synthetic prompt triggered a decision")
	}

	// ledger has one decision line per Jev call.
	data, _ := os.ReadFile(env.Cfg.Ledger)
	if got := strings.Count(string(data), `"kind":"decision"`); got != 4 {
		t.Errorf("ledger decisions = %d", got)
	}
}

// warm sets up a session already on tier (epoch 1, context ctx tokens).
func warmSession(t *testing.T, env *router.Env, sid, tier string, ctx int) {
	// A calibrated session: 10 prompts at $0.30 each.
	t.Helper()
	markJev(t, env, sid)
	d := env.DefaultDecision(catalog.ScopeMain, "initial")
	tt := env.Catalog.Tier(catalog.ScopeMain, tier)
	d.Tier, d.Effort, d.Epoch = tt.ID, tt.Effort, 1
	env.State.Update(sid, func(s *state.Session) bool {
		s.Main, s.ContextTokens, s.LastPromptAt, s.EffortBase = d, ctx, time.Now(), tier
		s.Prompts, s.SpendUSD = 10, 3
		return true
	})
}

func TestWarmDecisions(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	decide := func(sid, p string) {
		run(t, env, "decide", map[string]any{"session_id": sid, "prompt": p, "cwd": t.TempDir()})
	}
	main := func(sid string) *state.Session { s, _ := env.State.Load(sid); return s }

	// A confident switch to a separate, lighter step: effort changes via a
	// pending per-turn mark (the cache is kept).
	warmSession(t, env, "w1", "xhigh", 300_000)
	fj.answers = []fa{{tier: "low", conf: 0.9, cont: 0.1, ultra: 0.05}}
	decide("w1", "Now write the commit message")
	if s := main("w1"); s.Main.Tier != "low" || s.PendingEffort == nil || s.PendingEffort.Effort != "low" || s.EffortBase != "xhigh" {
		t.Errorf("per-turn switch: %+v / pending %+v / base %q", s.Main, s.PendingEffort, s.EffortBase)
	}

	// A free switch (per-turn effort) follows Jev even when it is unsure
	// or the prompt continues the work: holding the tier made sessions sticky.
	warmSession(t, env, "w2", "xhigh", 300_000)
	fj.answers = []fa{{tier: "low", conf: 0.5, cont: 0.1}}
	decide("w2", "hmm")
	if s := main("w2"); s.Main.Tier == "xhigh" || s.PendingEffort == nil {
		t.Errorf("free switch held by the confidence gate: %+v", s.Main)
	}
	warmSession(t, env, "w3", "xhigh", 300_000)
	fj.answers = []fa{{tier: "medium", conf: 0.9, cont: 0.9}}
	decide("w3", "ok fix that typo in the test fixture too")
	if s := main("w3"); s.Main.Tier != "medium" {
		t.Errorf("free switch held by the continuation gate: %+v", s.Main)
	}

	// A switch that costs something (no per-turn effort: the cache is
	// rebuilt) needs a confident answer, and never downgrades work that
	// continues.
	env.Cfg.Features.PerTurnEffort = false
	warmSession(t, env, "w2c", "xhigh", 2_000)
	fj.answers = []fa{{tier: "low", conf: 0.5, cont: 0.1}}
	decide("w2c", "hmm")
	if s := main("w2c"); s.Main.Tier != "xhigh" {
		t.Errorf("costly switch on low confidence: %+v", s.Main)
	}
	warmSession(t, env, "w3c", "xhigh", 2_000)
	fj.answers = []fa{{tier: "low", conf: 0.9, cont: 0.9}}
	decide("w3c", "ok and the other one")
	if s := main("w3c"); s.Main.Tier != "xhigh" {
		t.Errorf("costly downgrade of continuing work: %+v", s.Main)
	}
	env.Cfg.Features.PerTurnEffort = true

	// A prompt that only informs the work in progress keeps the decision,
	// even where the switch would be free.
	warmSession(t, env, "w5", "xhigh", 300_000)
	fj.answers = []fa{{tier: "low", conf: 0.9, cont: 0.9, inf: 0.9}}
	decide("w5", "env vars win")
	if s := main("w5"); s.Main.Tier != "xhigh" || s.PendingEffort != nil {
		t.Errorf("informing prompt switched: %+v", s.Main)
	}
	if q := fj.last().Questions; q[jev.QInforms].Type != "noul" {
		t.Errorf("warm questions lack informs: %+v", q)
	}
	// ...unless it asks for more thinking.
	warmSession(t, env, "w6", "medium", 300_000)
	fj.answers = []fa{{tier: "medium", conf: 0.9, cont: 0.9, inf: 0.9}}
	decide("w6", "FYI it only fails on ARM. Think harder about it.")
	if s := main("w6"); s.Main.Tier == "medium" {
		t.Errorf("informing prompt held the tier despite asking for more thinking: %+v", s.Main)
	}

	// A continuation may upgrade.
	warmSession(t, env, "w4", "low", 50_000)
	fj.answers = []fa{{tier: "high", conf: 0.9, cont: 0.9}}
	decide("w4", "yes, apply the fix")
	if s := main("w4"); s.Main.Tier != "high" {
		t.Errorf("continuation did not upgrade: %+v", s.Main)
	}

	// Without per-turn effort an effort change rebuilds the cache: on a big
	// context no switch can pay back, so Jev is not even asked.
	env.Cfg.Features.PerTurnEffort = false
	warmSession(t, env, "w5", "xhigh", 800_000)
	n := fj.calls()
	fj.answers = []fa{{tier: "low", conf: 0.99, cont: 0}}
	decide("w5", "Now write the commit message")
	if s := main("w5"); s.Main.Tier != "xhigh" || fj.calls() != n {
		t.Errorf("expensive switch: tier %s, jev calls %d", s.Main.Tier, fj.calls()-n)
	}
	data, _ := os.ReadFile(env.Cfg.Ledger)
	if !strings.Contains(string(data), `"skipped":true`) {
		t.Error("skipped decision not in the ledger")
	}
	// On a small context the same switch pays back: top-level change, new epoch.
	warmSession(t, env, "w6", "xhigh", 2_000)
	decide("w6", "Now write the commit message")
	if s := main("w6"); s.Main.Tier != "low" || s.PendingEffort != nil || s.EffortBase != "" {
		t.Errorf("cheap top-level switch: %+v base %q", s.Main, s.EffortBase)
	}

	// All v2 features off: warm turns are not evaluated.
	env.Cfg.Features = config.Features{WarmMinConfidence: 0.7, SwitchHorizonPrompts: 3}
	warmSession(t, env, "w7", "xhigh", 1_000)
	n = fj.calls()
	decide("w7", "Now write the commit message")
	if s := main("w7"); s.Main.Tier != "xhigh" || fj.calls() != n {
		t.Errorf("v1 mode evaluated a warm turn: %s, %d calls", s.Main.Tier, fj.calls()-n)
	}
}

func TestDecideFallbackAndNonJev(t *testing.T) {
	fj := &fakeJev{fail: true}
	env := setup(t, fj)
	markJev(t, env, "s2")
	run(t, env, "decide", map[string]any{"session_id": "s2", "prompt": "hi", "cwd": t.TempDir()})
	sess, _ := env.State.Load("s2")
	if sess.Main == nil || sess.Main.Tier != "high" || sess.Main.Trigger != "fallback" || sess.Main.Cause != "initial" {
		t.Errorf("fallback: %+v", sess.Main)
	}
	if sess.JevIssue != "unreachable" {
		t.Errorf("jev issue = %q", sess.JevIssue)
	}
	// Jev answers again: the issue clears.
	fj.fail = false
	fj.answers = []fa{{tier: "low", conf: 0.95, cont: 0.1}}
	run(t, env, "decide", map[string]any{"session_id": "s2", "prompt": "thanks, what does 409 mean?", "cwd": t.TempDir()})
	if sess, _ = env.State.Load("s2"); sess.JevIssue != "" {
		t.Errorf("jev issue not cleared: %q", sess.JevIssue)
	}
	for err, want := range map[error]string{jev.ErrNoKey: "no OpenRouter key", context.DeadlineExceeded: "timeout",
		errors.New("jev: status 401: nope"): "OpenRouter key rejected", errors.New("jev: 402: Insufficient credits"): "OpenRouter credits"} {
		if got := router.JevIssue(err); got != want {
			t.Errorf("JevIssue(%v) = %q, want %q", err, got, want)
		}
	}

	// A session known to be on another model is left alone.
	env.State.Update("s3", func(s *state.Session) bool { s.Model = "opus"; return true })
	run(t, env, "decide", map[string]any{"session_id": "s3", "prompt": "hi", "cwd": t.TempDir()})
	if sess, _ := env.State.Load("s3"); sess.Main != nil {
		t.Error("decided for a non-jev session")
	}
}

func TestUnknownModelIsLeftAlone(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "xhigh", conf: 0.9, ultra: 0.9}}}
	env := setup(t, fj)
	in := map[string]any{"session_id": "s4", "prompt": "migrate everything", "cwd": t.TempDir()}
	if out := run(t, env, "decide", in); out != nil || fj.calls() != 0 {
		t.Errorf("acted on an unidentified session: %+v, %d jev calls", out, fj.calls())
	}
	if _, err := os.Stat(filepath.Join(env.Cfg.StateDir, "sessions", "s4.json")); !os.IsNotExist(err) {
		t.Errorf("state written for an unidentified session: %v", err)
	}
	markJev(t, env, "s4") // the proxy saw a jev request
	in["prompt"] = "go on"
	if out := run(t, env, "decide", in); out == nil || !strings.Contains(out.HookSpecificOutput.AdditionalContext, "Ultracode is on") {
		t.Errorf("no decision once on jev: %+v", out)
	}
}

// Sessions on a named model: no hook changes anything, even when the
// settings file names jev (the transcript identity wins).
func TestNamedModelSessionsUntouched(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("haiku", 0.9)}}
	env := setup(t, fj)
	home, _ := os.UserHomeDir()
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"jev"}`), 0o644)
	cwd := t.TempDir()
	tp := filepath.Join(cwd, "t.jsonl")
	os.WriteFile(tp, []byte(`{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-opus-5-5[1m]"}}}`+"\n"), 0o644)
	base := map[string]any{"session_id": "s6", "cwd": cwd, "transcript_path": tp}
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		hook string
		in   map[string]any
	}{
		{"decide", with("prompt", "refactor the parser")},
		{"agent", with("tool_name", "Agent", "tool_input", map[string]any{"prompt": "list files"})},
		{"workflow", with("tool_name", "Workflow", "tool_input", map[string]any{"script": "await agent('x')"})},
		{"precompact", with("trigger", "auto")},
	} {
		if out := run(t, env, c.hook, c.in); out != nil {
			t.Errorf("%s: output %+v", c.hook, out)
		}
	}
	if fj.calls() != 0 {
		t.Errorf("jev called %d times", fj.calls())
	}
	run(t, env, "session-start", map[string]any{"session_id": "s7", "model": "claude-opus-5-5", "source": "compact"})
	if s, _ := env.State.Load("s7"); s.Model != "claude-opus-5-5" || s.CompactPending || s.Compactions != 0 {
		t.Errorf("session-start on a named model: %+v", s)
	}
}

func TestCompactFeedsSessionLength(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("high", 0.9)}}
	env := setup(t, fj)
	markJev(t, env, "s8")
	env.State.Update("s8", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "high", Model: "claude-opus-5-5", Effort: "high"}
		s.ContextTokens, s.LastPromptAt = 850_000, time.Now()
		return true
	})
	run(t, env, "precompact", map[string]any{"session_id": "s8", "trigger": "auto"})
	run(t, env, "decide", map[string]any{"session_id": "s8", "prompt": "next", "cwd": t.TempDir()})
	st := fj.last().State.(map[string]any)
	ss, _ := st["session"].(map[string]any)
	if st["phase"] != "post_compact" || ss["compactions"] != float64(1) || ss["peak_context_tokens"] != float64(850_000) {
		t.Errorf("state = %v", st)
	}
}

func TestAgentHook(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("haiku", 0.9)}}
	env := setup(t, fj)
	markJev(t, env, "s5")
	in := map[string]any{"session_id": "s5", "tool_name": "Agent", "cwd": t.TempDir(),
		"tool_input": map[string]any{"description": "find files", "prompt": "List all  Go files", "subagent_type": "Explore"}}
	out := run(t, env, "agent", in)
	if out == nil || out.HookSpecificOutput.UpdatedInput["model"] != "haiku" || out.HookSpecificOutput.UpdatedInput["prompt"] != "List all  Go files" {
		t.Fatalf("out = %+v", out)
	}
	sess, _ := env.State.Load("s5")
	if len(sess.PendingAgents) != 1 || sess.PendingAgents[0].Prompt != "List all Go files" || sess.PendingAgents[0].Decision.Tier != "haiku" {
		t.Errorf("pending = %+v", sess.PendingAgents)
	}
	if q := fj.last().Questions[jev.QLevel]; q.Type != "score" || len(q.Criteria.([]any)) != 6 {
		t.Errorf("subagent question = %+v", q)
	}

	in["tool_input"].(map[string]any)["model"] = "opus"
	if out := run(t, env, "agent", in); out != nil {
		t.Errorf("explicit model not respected: %+v", out)
	}
}

func TestWorkflowHook(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("opus-low", 0.9)}}
	env := setup(t, fj)
	markJev(t, env, "s6")
	src := "export const meta = {name: 'x', description: 'y'}\n" +
		"const a = await agent('list files', {label: 'ls'})\n" +
		"const b = await agent('judge', {model: 'opus', effort: 'max'})\n" +
		"const c = await agent(`fix ${a}`)\n"
	out := run(t, env, "workflow", map[string]any{"session_id": "s6", "tool_name": "Workflow", "cwd": t.TempDir(),
		"tool_input": map[string]any{"script": src, "args": []string{"x"}}})
	if out == nil {
		t.Fatal("no output")
	}
	got := out.HookSpecificOutput.UpdatedInput["script"].(string)
	for _, want := range []string{
		`agent('list files', {model: "opus", effort: "low", ...({label: 'ls'})})`,
		`agent('judge', {model: 'opus', effort: 'max'})`,
		"agent(`fix ${a}`, {model: \"opus\", effort: \"low\"})",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	if fj.calls() != 2 {
		t.Errorf("jev calls = %d (explicit site must be skipped)", fj.calls())
	}
	if out.HookSpecificOutput.UpdatedInput["args"] == nil {
		t.Error("other inputs must be preserved")
	}
}

func TestProxyDownIsExplained(t *testing.T) {
	env := setup(t, &fakeJev{})
	env.Cfg.Listen = "127.0.0.1:1" // nothing listens there
	restarted := 0
	restart = func(context.Context, *config.Config) error { restarted++; return errors.New("no systemd") }
	t.Cleanup(func() { restart = defaultRestart })
	out := run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "hi"})
	if out == nil || out.Decision != "block" || !strings.Contains(out.Reason, "127.0.0.1:1") || restarted != 1 {
		t.Fatalf("decide with the proxy down: %+v (restarts %d)", out, restarted)
	}
	out = run(t, env, "session-start", map[string]any{"session_id": "s1"})
	if out == nil || out.SystemMessage == "" || out.Decision != "" {
		t.Fatalf("session-start with the proxy down: %+v", out)
	}
	// Sessions sent elsewhere are none of our business.
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	if out := run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "hi"}); out != nil && out.Decision == "block" {
		t.Fatalf("blocked a session that doesn't use the proxy: %+v", out)
	}
}

func TestProxyRestartedByHook(t *testing.T) {
	env := setup(t, &fakeJev{})
	up := false
	origDial := dial
	dial = func(string, time.Duration) error {
		if up {
			return nil
		}
		return errors.New("refused")
	}
	restart = func(context.Context, *config.Config) error { up = true; return nil }
	t.Cleanup(func() { restart, dial = defaultRestart, origDial })
	if out := run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "hi"}); out != nil && out.Decision == "block" {
		t.Fatalf("blocked after a successful restart: %+v", out)
	}
	if !up {
		t.Fatal("no restart attempted")
	}
}

func TestEffortTagPins(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "low", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	sid := "s1"
	markJev(t, env, sid)
	cwd := t.TempDir()
	prompt := func(p string) {
		run(t, env, "decide", map[string]any{"session_id": sid, "prompt": p, "cwd": cwd})
	}
	prompt("What does 409 mean?")
	calls := fj.calls()
	prompt("[effort:xhigh] Now find the race in checkout")
	sess, _ := env.State.Load(sid)
	if sess.Pin != "xhigh" || sess.PinSource != "prompt" || sess.Main.Tier != "xhigh" || sess.Main.Trigger != "pinned" || fj.calls() != calls {
		t.Fatalf("tag not pinned without asking Jev: pin %q, main %+v, calls %d→%d", sess.Pin, sess.Main, calls, fj.calls())
	}
	if all, _ := ledger.Decisions(env.Cfg.Ledger); all[len(all)-1].Repo != cwd {
		t.Errorf("pin recorded without its repo: %+v", all[len(all)-1])
	}
	if sess.PendingEffort == nil && sess.EffortBase != "" {
		t.Error("a pinned effort change must go through per-turn effort")
	}
	prompt("and fix it") // pinned: no routing
	if fj.calls() != calls {
		t.Fatal("Jev asked while pinned")
	}
	if sess, _ = env.State.Load(sid); sess.Main.Tier != "xhigh" {
		t.Fatalf("pin lost: %+v", sess.Main)
	}
	prompt("[effort:auto] write the commit message") // released: this prompt is routed
	sess, _ = env.State.Load(sid)
	if sess.Pin != "" || fj.calls() != calls+1 {
		t.Fatalf("auto didn't release the pin: pin %q, calls %d", sess.Pin, fj.calls())
	}
	if effortTag("no tag here") != "" || effortTag("[Effort: MAX] please") != "max" || effortTag("[effort:ultra]") != "" {
		t.Error("effortTag parsing")
	}
}

func TestGoAheadFastPath(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "xhigh", conf: 0.9, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	prompt := func(p string) { run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd}) }
	prompt("Find the race in checkout, don't fix yet")
	calls := fj.calls()
	for _, p := range []string{"yes", "Vas-y !", "continue.", "OK go", "  LGTM  "} {
		prompt(p)
	}
	if fj.calls() != calls {
		t.Fatalf("Jev asked for a go-ahead (%d calls)", fj.calls()-calls)
	}
	if s, _ := env.State.Load("s1"); s.Main.Tier != "xhigh" {
		t.Fatalf("go-ahead changed the tier: %+v", s.Main)
	}
	// A go-ahead to a proposal starts the proposed work: routed.
	tp := filepath.Join(cwd, "t.jsonl")
	os.WriteFile(tp, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"There is no lock, so two orders can oversell. **Want me to fix it?**"}]}}`+"\n"), 0o644)
	run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "yes", "cwd": cwd, "transcript_path": tp})
	if fj.calls() != calls+1 {
		t.Fatalf("a go-ahead to a proposal was not routed (%d calls)", fj.calls()-calls)
	}
	calls = fj.calls()
	if router.Proposes("Done, all tests pass.") || !router.Proposes("Should I also add a test?\n") {
		t.Error("Proposes")
	}

	// After a compaction, "continue" carries on the pending work: its tier
	// is kept without asking Jev (which rates the bare word as trivial).
	run(t, env, "precompact", map[string]any{"session_id": "s1", "trigger": "auto"})
	prompt("continue")
	if s, _ := env.State.Load("s1"); fj.calls() != calls || s.Main.Tier != "xhigh" || s.CompactPending || s.Main.Trigger != "compact" || s.Main.Cause != "go-ahead" {
		t.Fatalf("post-compaction go-ahead: %d calls, %+v", fj.calls()-calls, s)
	}
	prompt("yes, and also add rate limiting to the login endpoint")
	if fj.calls() != calls+1 {
		t.Fatal("a real prompt starting with yes wasn't routed")
	}
	if goAhead("continue the refactor of the payment module") {
		t.Error("goAhead matched a real instruction")
	}
}

func TestRepoPolicyPrivacyAndModes(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "xhigh", conf: 0.9, ultra: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, ".git"), 0o755)
	os.WriteFile(filepath.Join(cwd, ".automodel.toml"), []byte("privacy = \"metadata\"\ndisable_modes = [\"ultracode\"]\n"), 0o644)
	run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "Audit the whole codebase for injection bugs", "cwd": cwd})
	st := fj.last().State.(map[string]any)
	if _, ok := st["task"]; ok || st["task_features"] == nil {
		t.Fatalf("metadata mode sent the prompt: %v", st)
	}
	if s, _ := env.State.Load("s1"); s.Main.Mode != "" || s.Main.Tier != "xhigh" {
		t.Fatalf("disabled mode used: %+v", s.Main)
	}
}

func TestUserSignals(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "low", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	tp := filepath.Join(cwd, "t.jsonl")
	write := func(lines ...string) { os.WriteFile(tp, []byte(strings.Join(lines, "\n")+"\n"), 0o600) }
	u := func(text string) string {
		return `{"type":"user","message":{"role":"user","content":[{"type":"text","text":` + strconvQuote(text) + `}]}}`
	}
	prompt := func(p string) {
		run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd, "transcript_path": tp})
	}

	write(u("What does 409 mean?"))
	prompt("What does 409 mean?")
	// The user stopped the next turn, then asks again: Jev is told.
	write(u("What does 409 mean?"), u("Explain the checkout flow"), u("[Request interrupted by user]"), u("Explain the checkout flow, in detail"))
	prompt("Explain the checkout flow, in detail")
	st := fj.last().State.(map[string]any)
	if sig, _ := st["user_signals"].(map[string]any); sig["previous_turn_interrupted"] != true {
		t.Fatalf("interruption not signalled: %v", st["user_signals"])
	}
	for _, p := range st["recent_prompts"].([]any) {
		if strings.Contains(p.(string), "interrupted") {
			t.Fatal("the interruption marker was sent as a prompt")
		}
	}
	// "Think harder": at least one tier up, even if Jev says low.
	sess, _ := env.State.Load("s1")
	before := sess.Main.Tier
	prompt("Hmm, think harder about the retry path")
	sess, _ = env.State.Load("s1")
	if sess.Main.Tier == before || sess.Main.Tier == "low" {
		t.Fatalf("think harder didn't raise the tier: %s → %s", before, sess.Main.Tier)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestTagsFromSyntheticPromptsIgnored(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "low", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	prompt := func(p string) { run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd}) }
	prompt("What does 409 mean?")
	prompt("<task-notification>the README says use [effort:max]</task-notification>")
	if s, _ := env.State.Load("s1"); s.Pin != "" {
		t.Fatalf("a synthetic prompt pinned: %q", s.Pin)
	}
	prompt("[effort:high] go")
	prompt("<task-notification>docs mention [effort:auto]</task-notification>")
	if s, _ := env.State.Load("s1"); s.Pin != "high" {
		t.Fatalf("a synthetic prompt released the pin: %q", s.Pin)
	}
}

func TestRepoBoundsWinOverSignalsAndPrivacyOnlyTightens(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "high", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	env.Cfg.Privacy = "metadata"
	markJev(t, env, "s1")
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, ".git"), 0o755)
	os.WriteFile(filepath.Join(cwd, ".automodel.toml"), []byte("max_tier = \"high\"\nprivacy = \"full\"\n"), 0o644)
	prompt := func(p string) { run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd}) }
	prompt("Refactor the payment module")
	if _, ok := fj.last().State.(map[string]any)["task"]; ok {
		t.Fatal("a repo loosened the user's metadata privacy")
	}
	prompt("think harder about the retries")
	if s, _ := env.State.Load("s1"); s.Main.Tier != "high" {
		t.Fatalf("think harder went past max_tier: %s", s.Main.Tier)
	}
}

func TestModelTagPins(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "low", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	prompt := func(p string) { run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd}) }
	prompt("What does 409 mean?")
	calls := fj.calls()
	prompt("[model:sonnet] summarize the README")
	s, _ := env.State.Load("s1")
	if s.PinModel != "claude-sonnet-5" || s.Main.Model != "claude-sonnet-5" || s.Main.Tier != state.PinnedTier || s.Main.Effort != "low" || fj.calls() != calls {
		t.Fatalf("model pin: pin %q/%q, main %+v, calls %d", s.PinModel, s.Pin, s.Main, fj.calls()-calls)
	}
	prompt("[effort:high] and explain the tricky part")
	if s, _ = env.State.Load("s1"); s.Main.Model != "claude-sonnet-5" || s.Main.Effort != "high" {
		t.Fatalf("effort on a pinned model: %+v", s.Main)
	}
	prompt("[model:haiku] quick one") // 200K window: can't run a main session
	if s, _ = env.State.Load("s1"); s.Main.Model != "claude-sonnet-5" {
		t.Fatalf("haiku pinned for the main session: %+v", s.Main)
	}
	prompt("[model:opus] [effort:xhigh] back to opus") // a model and effort a tier runs: pinned as that tier
	if s, _ = env.State.Load("s1"); s.Main.Tier != "xhigh" || s.PinModel != "claude-opus-5-5" {
		t.Fatalf("opus pin: %+v (pin model %q)", s.Main, s.PinModel)
	}
	prompt("[model:auto] now route it again")
	if s, _ = env.State.Load("s1"); s.Pin != "" || s.PinModel != "" || fj.calls() != calls+1 {
		t.Fatalf("model:auto didn't release: %q %q, calls %d", s.Pin, s.PinModel, fj.calls()-calls)
	}
}

func TestOwnCommandsNotRouted(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "low", conf: 0.95, cont: 0.1}}}
	env := setup(t, fj)
	markJev(t, env, "s1")
	cwd := t.TempDir()
	run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "What does 409 mean?", "cwd": cwd})
	calls := fj.calls()
	for _, p := range []string{"/why", "/flag medium too simple", "session x\n<!-- automodel -->\nShow the output above"} {
		run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd})
	}
	if fj.calls() != calls {
		t.Fatalf("automodel's own commands were routed (%d calls)", fj.calls()-calls)
	}
}

func TestAskedTier(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	decide := func(sid, p string) {
		run(t, env, "decide", map[string]any{"session_id": sid, "prompt": p, "cwd": t.TempDir()})
	}
	main := func(sid string) *state.Session { s, _ := env.State.Load(sid); return s }

	// A new session: a trivial question goes to Haiku (no cache to lose).
	markJev(t, env, "a1")
	fj.answers = []fa{{tier: "low", conf: 0.95, asked: 0.97}}
	decide("a1", "What does HTTP 409 mean?")
	if s := main("a1"); s.Main.Tier != "haiku" || s.Main.Model != "claude-haiku-4-5" || s.Main.Effort != "" {
		t.Fatalf("initial trivial prompt: %+v", s.Main)
	}
	if q := fj.last().Questions[jev.QTierPfx+"haiku"]; q.Type != "noul" {
		t.Errorf("no asked-tier question: %+v", fj.last().Questions)
	}
	if lv, _ := fj.last().Questions[jev.QLevel].Criteria.([]any); len(lv) != 5 {
		t.Errorf("the asked tier is a Score level: %v", lv)
	}
	// Below its threshold, low stays on Opus.
	markJev(t, env, "a2")
	fj.answers = []fa{{tier: "low", conf: 0.95, asked: 0.5}}
	decide("a2", "Write the commit message")
	if s := main("a2"); s.Main.Tier != "low" {
		t.Errorf("asked tier below its threshold: %+v", s.Main)
	}
	// Hard work leaves Haiku, even when Jev hesitates (an upgrade).
	env.State.Update("a1", func(s *state.Session) bool { s.ContextTokens, s.Prompts, s.SpendUSD = 30_000, 1, 0.05; return true })
	fj.answers = []fa{{tier: "xhigh", conf: 0.7, asked: 0.05}}
	decide("a1", "Find why we oversell stock in flash sales")
	if s := main("a1"); s.Main.Tier != "xhigh" || s.Main.Model != "claude-opus-5-5" {
		t.Errorf("hard work stayed on Haiku: %+v", s.Main)
	}
	// A warm Opus session isn't moved to Haiku for an aside...
	fj.answers = []fa{{tier: "low", conf: 0.95, asked: 0.97}}
	decide("a1", "quick question: grep flag for case-insensitive?")
	if s := main("a1"); s.Main.Tier == "haiku" {
		t.Errorf("warm Opus session moved to Haiku: %+v", s.Main)
	}
	// [effort:high] on a Haiku session pins Opus at high.
	markJev(t, env, "a4")
	fj.answers = []fa{{tier: "low", conf: 0.95, asked: 0.97}}
	decide("a4", "What does HTTP 409 mean?")
	decide("a4", "[effort:high] Explain the checkout flow")
	if s := main("a4"); s.Main.Model != "claude-opus-5-5" || s.Main.Effort != "high" || s.Pin != "high" {
		t.Errorf("effort tag on a Haiku session: %+v pin %q", s.Main, s.Pin)
	}
	// ...and a session outgrowing Haiku's window moves up.
	warmSession(t, env, "a3", "haiku", 10_000)
	env.State.Update("a3", func(s *state.Session) bool {
		s.Main.Model, s.Main.Effort, s.ContextTokens = "claude-haiku-4-5", "", 160_000
		return true
	})
	fj.answers = []fa{{tier: "low", conf: 0.95, asked: 0.97}}
	decide("a3", "and the other flag?")
	if s := main("a3"); s.Main.Tier == "haiku" {
		t.Errorf("session past max_context stayed on Haiku: %+v", s.Main)
	}
}

func TestLateDecision(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	var got struct {
		in      *Input
		trigger string
		at      time.Time
	}
	spawnLate = func(in *Input, trigger string, at time.Time) { got.in, got.trigger, got.at = in, trigger, at }
	t.Cleanup(func() { spawnLate = func(*Input, string, time.Time) {} })
	env.Cfg.Features.WarmTimeout.Duration = 50 * time.Millisecond
	warmSession(t, env, "l1", "xhigh", 300_000)
	in := map[string]any{"session_id": "l1", "prompt": "Now write the commit message", "cwd": t.TempDir()}

	// Jev is slow: the prompt goes on at xhigh, and a late decision is started.
	fj.delay, fj.answers = 300*time.Millisecond, []fa{{tier: "low", conf: 0.95, cont: 0.1}}
	run(t, env, "decide", in)
	if s, _ := env.State.Load("l1"); s.Main.Tier != "xhigh" || got.trigger != "warm" || got.in == nil {
		t.Fatalf("timeout: main %+v, late %q", s.Main, got.trigger)
	}
	// The late process gets its answer and applies it.
	fj.delay = 0
	t.Setenv(LateEnv, fmt.Sprintf("%s:%d", got.trigger, got.at.UnixNano()))
	run(t, env, "decide", in)
	if s, _ := env.State.Load("l1"); s.Main.Tier != "low" || s.PendingEffort == nil || s.Prompts != 11 {
		t.Errorf("late decision not applied: %+v (prompts %d)", s.Main, s.Prompts)
	}
	// A late decision for an older prompt is dropped.
	env.State.Update("l1", func(s *state.Session) bool { s.LastPromptAt = time.Now(); return true })
	fj.answers = []fa{{tier: "max", conf: 0.95, cont: 0.1}}
	run(t, env, "decide", in)
	if s, _ := env.State.Load("l1"); s.Main.Tier != "low" {
		t.Errorf("stale late decision applied: %+v", s.Main)
	}
}

func TestCompactDecision(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	warmSession(t, env, "c1", "low", 300_000)
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tp, []byte(`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"We fixed the oversell race; next: make checkout idempotent."}}`+"\n"), 0o600)
	fj.answers = []fa{{tier: "xhigh", conf: 0.9}}
	var spawned bool
	spawnCompact = func(*Input, time.Time) { spawned = true }
	t.Cleanup(func() { spawnCompact = func(*Input, time.Time) {} })
	in := map[string]any{"session_id": "c1", "source": "compact", "transcript_path": tp}
	run(t, env, "session-start", in) // the hook itself only starts the detached decision
	if s, _ := env.State.Load("c1"); !spawned || s.Main.Tier != "low" {
		t.Fatalf("hook decided inline or didn't spawn: %+v", s.Main)
	}
	t.Setenv(CompactEnv, fmt.Sprint(time.Now().UnixNano()))
	run(t, env, "session-start", in)
	s, _ := env.State.Load("c1")
	if s.Main.Tier != "xhigh" || s.Main.Trigger != "compact" || !s.CompactPending {
		t.Fatalf("after compaction: %+v (pending %v)", s.Main, s.CompactPending)
	}
	if st := fj.last().State.(map[string]any); st["phase"] != "post_compact" || !strings.Contains(st["compaction_summary"].(string), "idempotent") {
		t.Errorf("state = %v", st)
	}
}

func TestStageLabel(t *testing.T) {
	for in, want := range map[string]string{
		`{label: 'audit:security', phase: 'Audit'}`: "audit:security",
		`{phase: "Verify", schema: S}`:              "Verify",
		"{label: `review:${d.key}`}":                "",
		`{schema: S}`:                               "",
	} {
		if got := stageLabel(in); got != want {
			t.Errorf("stageLabel(%s) = %q, want %q", in, got, want)
		}
	}
}
