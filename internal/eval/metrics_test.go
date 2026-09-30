package eval

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	data, err := os.ReadFile("../../catalog.toml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := catalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if errs := c.Validate(time.Now(), 3650).Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	return c
}

func res(id, want, got, decision string, p float64, run int) Result {
	return Result{Case: Case{ID: id, Scope: catalog.ScopeMain, Want: want, Accept: []string{want}},
		Run: run, Got: got, Decision: decision, Probs: map[string]float64{got: p}}
}

func TestScopeMetrics(t *testing.T) {
	c := testCatalog(t)
	rs := []Result{
		res("a", "low", "low", "low", 1, 0),
		res("b", "medium", "low", "high", 0.6, 0), // Jev skips medium both ways
		res("c", "medium", "high", "high", 0.9, 0),
		res("d", "high", "high", "high", 0.8, 0),
		res("a", "low", "medium", "low", 0.5, 1), // unstable across runs
	}
	s := ScopeMetrics(c, catalog.ScopeMain, rs)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if s.N != 5 || s.Cases != 4 {
		t.Fatalf("N=%d cases=%d", s.N, s.Cases)
	}
	if !near(s.Exact, 2.0/5) || !near(s.DecisionExact, 3.0/5) {
		t.Errorf("exact %v, decision exact %v", s.Exact, s.DecisionExact)
	}
	if got := s.Class["medium"]; got.Labels != 2 || got.RecallDecision != 0 || !near(got.LabelShare, 0.4) || got.Decision != 0 {
		t.Errorf("medium class %+v", got)
	}
	if id, r := s.MinRecall(); id != "medium" || r != 0 {
		t.Errorf("min recall %s %v", id, r)
	}
	if id, g := s.MaxShareGap(); (id != "medium" && id != "high") || !near(g, 0.4) {
		t.Errorf("share gap %s %v", id, g)
	}
	// Ranks: b low vs medium = 1, c high vs medium = 1, a(run 1) medium vs low = 1.
	if !near(s.MAE, 3.0/5) || s.Over != 2 || s.Under != 0 {
		t.Errorf("MAE %v over %d under %d", s.MAE, s.Over, s.Under)
	}
	if s.Confusion["medium"]["low"] != 1 || s.ConfusionDecision["medium"]["high"] != 2 {
		t.Errorf("confusion %v / %v", s.Confusion, s.ConfusionDecision)
	}
	if !near(s.Unstable, 0.25) {
		t.Errorf("unstable %v", s.Unstable)
	}
	// Bins: 1.0 (right), 0.6 (wrong), 0.9 (wrong), 0.8 (right), 0.5 (wrong).
	want := (1*0 + 0.6 + 0.9 + 0.2 + 0.5) / 5
	if !near(s.ECE, want) {
		t.Errorf("ECE %v, want %v", s.ECE, want)
	}
	var b strings.Builder
	PrintScope(&b, s)
	if !strings.Contains(b.String(), "| medium | 2 | 40% | 20% | 0% | 0% | 0% |") {
		t.Errorf("table:\n%s", b.String())
	}
}

func TestGate(t *testing.T) {
	c := testCatalog(t)
	var rs []Result
	// 40 medium labels: 20 routed to high (the collapse), 20 right; 40 high, right.
	for i := 0; i < 40; i++ {
		got := "medium"
		if i%2 == 0 {
			got = "high"
		}
		rs = append(rs, res(fmt.Sprint("m", i), "medium", got, got, 0.9, 0), res(fmt.Sprint("h", i), "high", "high", "high", 0.9, 0))
	}
	fails := ScopeMetrics(c, catalog.ScopeMain, rs).Check(DefaultGate)
	joined := strings.Join(fails, "; ")
	for _, want := range []string{"decision exact 75%", "medium recall 50%", "high gets 75% of decisions for 50% of labels"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	var ok []Result
	for _, r := range rs {
		r.Got, r.Decision = r.Want, r.Want
		ok = append(ok, r)
	}
	if fails := ScopeMetrics(c, catalog.ScopeMain, ok).Check(DefaultGate); len(fails) > 0 {
		t.Errorf("a perfect run fails the gate: %v", fails)
	}
}

func TestRelationAndExplicitMetrics(t *testing.T) {
	c := testCatalog(t)
	rel := func(top string, p float64) map[string]float64 {
		m := map[string]float64{}
		for _, r := range catalog.Relations {
			m[r] = (1 - p) / float64(len(catalog.Relations)-1)
		}
		m[top] = p
		return m
	}
	main := catalog.ScopeMain
	rs := []Result{
		{Case: Case{ID: "a", Scope: main, Want: "xhigh", Relation: "extend"}, Got: "medium", Decision: "xhigh", WorkTier: "xhigh", RelP: rel("extend", 0.9)},
		// A side question read as a wrap-up, and lowered: the gate fails.
		{Case: Case{ID: "b", Scope: main, Want: "xhigh", Relation: "side_question"}, Got: "low", Decision: "low", WorkTier: "xhigh", RelP: rel("wrap_up", 0.7)},
		{Case: Case{ID: "c", Scope: main, Want: "low", Relation: "wrap_up", Explicit: &Explicit{Effort: "low"}}, Got: "low", Decision: "low", WorkTier: "xhigh",
			RelP: rel("wrap_up", 0.95), ExplicitP: map[string]float64{"effort_low": 0.9}},
		// Ultracode asked but not confirmed, Sonnet asked but not found, max only mentioned but confirmed.
		{Case: Case{ID: "d", Scope: main, Want: "high", Explicit: &Explicit{Mode: "ultracode", Model: "sonnet"}, Modes: map[string]bool{"ultracode": true}},
			Got: "high", Decision: "high", ExplicitP: map[string]float64{"mode_ultracode": 0.5, "effort_max": 0.85}},
		// Back to the paused work, below it; Fable only mentioned, at 0.85
		// (below the model threshold); Opus asked on an Opus session (no request).
		{Case: Case{ID: "e", Scope: main, Want: "xhigh", Relation: "resume", Explicit: &Explicit{Model: "opus"}}, Got: "low", Decision: "high", WorkTier: "low", PausedTier: "xhigh",
			RelP: rel("resume", 0.8), ExplicitP: map[string]float64{"model_claude-fable-5-1": 0.85}},
		// A prompt typed mid-turn is held whatever its relation; its work runs on Sonnet.
		{Case: Case{ID: "f", Scope: main, Want: "high", Relation: "new_task", State: map[string]any{"mid_turn": true, "work_in_progress": map[string]any{"level": "high", "model": "sonnet"}}},
			Got: "low", Decision: "high", Model: "claude-sonnet-5-5", WorkTier: "high", RelP: rel("new_task", 0.9)},
	}
	for i := range rs {
		rs[i].Accept, rs[i].Probs = []string{rs[i].Want}, map[string]float64{rs[i].Got: 1}
	}
	s := Summarize(c, rs)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if r := s.Relation; r.N != 5 || r.Right != 4 || r.Confusion["side_question"]["wrap_up"] != 1 {
		t.Errorf("relation %+v", r)
	}
	if e := s.Explicit; e.TP != 1 || e.FP != 1 || e.FN != 2 || !near(e.Precision(), 0.5) || !near(e.Recall(), 1.0/3) {
		t.Errorf("explicit %+v", e)
	}
	if k := s.Explicit.ByKind; k["effort"].FP != 1 || k["effort"].TP != 1 || k["mode"].FN != 1 || k["model"].FN != 1 || k["model"].FP != 0 || k["model"].Threshold != 0.9 {
		t.Errorf("explicit by kind: effort %+v, mode %+v, model %+v", k["effort"], k["mode"], k["model"])
	}
	// a: right; b: below its work; e: below the paused work; f: mid-turn, held.
	if f := s.Follow; f.N != 4 || f.Right != 2 {
		t.Errorf("follow-ups %+v", f)
	}
	// d asks for Sonnet (not decided), f follows up work on Sonnet.
	if m := s.Model; m.N != 2 || m.Right != 1 {
		t.Errorf("model decisions %+v", m)
	}
	if m := s.ModeDecision["ultracode"]; m == nil || m.N != 1 || m.Right != 0 {
		t.Errorf("mode decisions %+v", m)
	}
	st := s.Scopes[main]
	if st.FollowUnder != 2 {
		t.Errorf("below the label on follow-ups: %d", st.FollowUnder)
	}
	fails := strings.Join(s.Check(DefaultGate), "; ")
	for _, want := range []string{"2 decisions below the label on follow-ups", "2 decisions below the work in progress", "1 effort requests confirmed"} {
		if !strings.Contains(fails, want) {
			t.Errorf("gate: missing %q in %s", want, fails)
		}
	}
	if strings.Contains(fails, "model requests") {
		t.Errorf("gate: %s", fails)
	}
	var b strings.Builder
	PrintSummary(&b, s)
	for _, want := range []string{"relation: 4/5 right (80%)", "| side_question | 0 | 0 | 0 | 0 | 0 | 1 | 0 |", "explicit requests: precision 50%, recall 33%",
		"model @0.90: precision 0%, recall 0% (0 right, 0 false, 1 missed)", "2/4 decisions below the work they hold", "model the work runs on (router decision): 1/2 right",
		"mode ultracode on/off (router decision): 0/1 right"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}

// A case is judged as the hooks would: the work in progress from the case
// (else its decision in force), the prompt's requests, the mid-turn flag
// (never sent to Jev).
func TestCaseSetup(t *testing.T) {
	c := testCatalog(t)
	st, req := setup(c, Case{Scope: catalog.ScopeMain, Warm: true, State: map[string]any{
		"phase": "warm", "task": "passe en low pour la suite", "current": map[string]any{"tier": "xhigh", "mode": "ultracode"}, "mid_turn": true}})
	if _, ok := st["mid_turn"]; ok {
		t.Error("mid_turn sent to Jev")
	}
	if wip, _ := st["work_in_progress"].(map[string]any); wip["level"] != "xhigh" {
		t.Errorf("state = %v", st)
	}
	if w := req.Work; w == nil || w.Tier != "xhigh" || w.Mode != "ultracode" || !req.MidTurn || req.Current == nil || len(req.Explicit) != 1 || req.Explicit[0].Tier != "low" {
		t.Errorf("request = %+v", req)
	}
	st, req = setup(c, Case{Scope: catalog.ScopeMain, State: map[string]any{
		"phase": "resumed", "task": "on reprend", "current": map[string]any{"tier": "low"}, "work_in_progress": map[string]any{"goal": "Fix the race", "level": "high"}}})
	if w := req.Work; w == nil || w.Tier != "high" || w.Goal != "Fix the race" || req.Current != nil || req.MidTurn {
		t.Errorf("labeled work in progress: %+v", req)
	}
	if _, req = setup(c, Case{Scope: catalog.ScopeMain, State: map[string]any{"phase": "initial", "task": "ultrathink: design the sharding"}}); req.Work != nil || req.MinTier != "xhigh" {
		t.Errorf("initial: %+v", req)
	}
	// Work on a model outside the tiers, and paused work: Jev sees their
	// goal and level only; efforts map to the tiers' model.
	st, req = setup(c, Case{Scope: catalog.ScopeMain, Warm: true, State: map[string]any{
		"phase": "warm", "task": "passe en xhigh et reprends la migration", "current": map[string]any{"effort": "high", "model": "Sonnet 5.5"},
		"work_in_progress": map[string]any{"goal": "Export the invoices", "level": "high", "model": "sonnet"},
		"paused_work":      map[string]any{"goal": "Migrate the ledger to v2", "level": "xhigh", "mode": "ultracode"}}})
	if w, p := req.Work, req.Paused; w == nil || w.Model != "claude-sonnet-5-5" || w.Tier != "high" || p == nil || p.Tier != "xhigh" || p.Mode != "ultracode" || p.Goal != "Migrate the ledger to v2" {
		t.Errorf("work %+v, paused %+v", req.Work, req.Paused)
	}
	if req.Current == nil || req.Current.Tier != state.PinnedTier || req.Current.Model != "claude-sonnet-5-5" || len(req.Explicit) != 1 || req.Explicit[0].Tier != "xhigh" {
		t.Errorf("request = %+v", req)
	}
	if wip, pw := st["work_in_progress"].(map[string]any), st["paused_work"].(map[string]any); len(wip) != 2 || len(pw) != 2 || pw["level"] != "xhigh" {
		t.Errorf("sent %v, %v", wip, pw)
	}
}

// Saved answers are judged again under another policy without asking Jev:
// the same answers, another threshold, another decision.
func TestRejudge(t *testing.T) {
	c := testCatalog(t)
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	rel := map[string]float64{"new_task": 0.55, "extend": 0.45}
	rs := []Result{{Case: Case{ID: "a", Scope: catalog.ScopeMain, Warm: true, Want: "xhigh", Relation: "extend",
		State: map[string]any{"phase": "warm", "task": "and the retry storm too", "current": map[string]any{"tier": "xhigh"}}},
		Probs: map[string]float64{"low": 0.1, "medium": 0.8, "high": 0.1}, Conf: 0.7, RelP: rel}}
	if r := Rejudge(env, rs)[0]; r.Decision != "xhigh" || r.WorkTier != "xhigh" || !strings.HasPrefix(r.Hold, "follow-up of the work in progress") {
		t.Errorf("held: %+v", r)
	}
	c.Meta.RelationSeparateP = 0.5
	if r := Rejudge(env, rs)[0]; r.Decision != "medium" || r.Hold != "" {
		t.Errorf("separate at 0.5: %+v", r)
	}
}

func TestFilter(t *testing.T) {
	cs := []Case{{ID: "a"}, {ID: "b", Split: "test"}, {ID: "c", Split: "train"}}
	if got := Filter(cs, "train"); len(got) != 2 || got[0].ID != "a" || got[1].ID != "c" {
		t.Errorf("train %v", got)
	}
	if got := Filter(cs, "test"); len(got) != 1 || got[0].ID != "b" {
		t.Errorf("test %v", got)
	}
	if got := Filter(cs, "all"); len(got) != 3 {
		t.Errorf("all %v", got)
	}
}
