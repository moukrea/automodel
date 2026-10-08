package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/jev"
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
	c.Meta.ExplicitP, c.Meta.ExplicitEffortP, c.Meta.ExplicitModelP = 0.8, 0, 0.9 // an effort at explicit_threshold
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
	for _, want := range []string{"relation: 4/5 right (80%)", "| side_question | 0 | 0 | 0 | 0 | 0 | 0 | 1 | 0 |", "explicit requests: precision 50%, recall 33%",
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
	if w := req.Work; w == nil || w.Tier != "xhigh" || w.Mode != "ultracode" || !req.MidTurn || req.Current == nil || explicitTier(req, "low") != "low" {
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
	if req.Current == nil || req.Current.Tier != state.PinnedTier || req.Current.Model != "claude-sonnet-5-5" || explicitTier(req, "xhigh") != "xhigh" {
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
	c.Meta.RelationSeparateP = 0.6
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	rel := map[string]float64{"new_task": 0.55, "extend": 0.45}
	rs := []Result{{Case: Case{ID: "a", Scope: catalog.ScopeMain, Warm: true, Want: "xhigh", Relation: "extend",
		State: map[string]any{"phase": "warm", "task": "and the retry storm too", "current": map[string]any{"tier": "xhigh"},
			"work_in_progress": map[string]any{"goal": "stop the retry loop from hammering the API", "level": "xhigh"}}},
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

// A follow-up counts as below the work's level only below every tier its
// label accepts: a bare "continue" after a compaction, labeled max with
// xhigh acceptable, decided at xhigh (the go-ahead fast path) is fine.
func TestFollowUnderUsesAccept(t *testing.T) {
	c := testCatalog(t)
	r := res("jepsen", "max", "max", "xhigh", 1, 0)
	r.Accept, r.Relation = []string{"xhigh", "max"}, catalog.RelationContinue
	low := res("below", "xhigh", "low", "high", 1, 0)
	low.Accept, low.Relation = []string{"xhigh", "max"}, catalog.RelationExtend
	s := ScopeMetrics(c, catalog.ScopeMain, []Result{r, low})
	if s.Under != 2 || s.FollowUnder != 1 {
		t.Errorf("under %d, on follow-ups %d", s.Under, s.FollowUnder)
	}
}

// The mode's on/off decision is part of the gate: its recall on the cases
// labeled on, and on those labeled off, is held to MinRecall.
func TestModeGate(t *testing.T) {
	c := testCatalog(t)
	var rs []Result
	for i := 0; i < 24; i++ {
		on, off := res(fmt.Sprint("on", i), "xhigh", "xhigh", "xhigh", 1, 0), res(fmt.Sprint("off", i), "high", "high", "high", 1, 0)
		on.Modes, off.Modes = map[string]bool{"ultracode": true}, map[string]bool{"ultracode": false}
		if i < 12 {
			on.Mode = "ultracode"
		}
		rs = append(rs, on, off)
	}
	s := Summarize(c, rs)
	fails := strings.Join(s.Check(DefaultGate), "; ")
	if !strings.Contains(fails, "mode ultracode on right on 12/24 cases labeled on") || strings.Contains(fails, "labeled off") {
		t.Errorf("gate: %s", fails)
	}
}

// The eval runs the hooks' own go-ahead fast path (router.Carried): typed
// mid-turn it keeps the mode the turn runs with; on a warm cache it
// doesn't move to the work's model; after a detour it goes back to the
// paused work.
func TestEvalGoAhead(t *testing.T) {
	c := testCatalog(t)
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	judge := func(cs Case) Result {
		t.Helper()
		_, req := setup(c, cs)
		ans, ids := levelAnswer(c, "low")
		r := Result{Case: cs}
		r.judge(env, req, ans, ids)
		return r
	}
	st := func(kv ...any) map[string]any {
		m := map[string]any{"phase": "warm", "task": "go", "last_assistant": "Porting handler 7 of 18."}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	}
	r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: st("mid_turn", true,
		"current", map[string]any{"tier": "xhigh", "mode": "ultracode"}, "work_in_progress", map[string]any{"goal": "migrate the handlers", "level": "xhigh", "mode": ""})})
	if r.Decision != "xhigh" || r.Mode != "ultracode" || r.Kept != "go-ahead" {
		t.Errorf("mid-turn go-ahead: %+v", r)
	}
	r = judge(Case{Scope: catalog.ScopeMain, Warm: true, State: st(
		"current", map[string]any{"tier": "low"}, "work_in_progress", map[string]any{"goal": "export the invoices", "level": "high", "model": "sonnet"})})
	if r.Decision != "low" || r.Model != "" {
		t.Errorf("go-ahead on a warm cache, work on another model: %+v", r)
	}
	r = judge(Case{Scope: catalog.ScopeMain, Warm: true, State: st(
		"current", map[string]any{"tier": "low"}, "work_in_progress", map[string]any{"goal": "fix the README typo", "level": "low"},
		"paused_work", map[string]any{"goal": "migrate the handlers", "level": "xhigh", "mode": "ultracode"})})
	if r.Decision != "xhigh" || r.Mode != "ultracode" {
		t.Errorf("go-ahead after a detour: %+v", r)
	}
	// The same once the detour was wrapped up: carried back to the paused
	// work. A go-ahead to a proposal after a detour is routed with the
	// relation question: back to the paused work, a wrap-up step of the
	// detour at its own level, or more of the detour.
	done := func(last string) Case {
		return Case{Scope: catalog.ScopeMain, Warm: true, State: st("last_assistant", last,
			"current", map[string]any{"tier": "low"}, "work_in_progress", map[string]any{"goal": "fix the README typo", "level": "low", "done": true},
			"paused_work", map[string]any{"goal": "migrate the handlers", "level": "xhigh", "mode": "ultracode"})}
	}
	if r = judge(done("Committed as docs: fix the typo.")); r.FastPath != "go-ahead" || r.Decision != "xhigh" || r.Mode != "ultracode" {
		t.Errorf("go-ahead after a wrapped-up detour: %+v", r)
	}
	proposal := func(last, rel string) Result {
		t.Helper()
		cs := done(last)
		_, req := setup(c, cs)
		if fp := fastPath(env, cs, req); fp != "" {
			t.Errorf("%q: fast path %q", last, fp)
		}
		ans, ids := levelAnswer(c, "low")
		ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: map[string]float64{rel: 0.9, "continue": 0.05, "new_task": 0.05}, Confidence: 0.9}
		offer := 0.95 // the assistant offered more of the detour
		if rel == "resume" {
			offer = 0.05
		}
		ans[jev.QOffer] = jev.Answer{Type: "noul", Noul: &offer}
		r := Result{Case: cs}
		r.judge(env, req, ans, ids)
		return r
	}
	if r = proposal("Committed. Shall I get back to the handlers?", "resume"); r.Decision != "xhigh" || r.Mode != "ultracode" {
		t.Errorf("yes to going back after a wrapped-up detour: %+v", r)
	}
	if r = proposal("Committed. Want me to push it?", "wrap_up"); r.Decision != "low" || r.Mode != "" {
		t.Errorf("yes to pushing the wrapped-up detour: %+v", r)
	}
	if r = proposal("Committed. Want me to fix the two other typos too?", "extend"); r.Decision != "low" || r.Mode != "" {
		t.Errorf("yes to more of the wrapped-up detour: %+v", r)
	}
	// A question that offers nothing of the detour, or Jev unsure it did
	// (the offer question under its bar): back to the paused work, as the
	// hooks do (BackFirst), whatever the relation reads.
	for _, tc := range []struct{ last, rel string }{
		{"Committed. Anything else?", "continue"},
		{"Committed. Should I push it? It would also push the README fix.", "wrap_up"},
	} {
		cs := done(tc.last)
		_, req := setup(c, cs)
		ans, ids := levelAnswer(c, "low")
		ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: map[string]float64{tc.rel: 0.9, "resume": 0.05, "new_task": 0.05}, Confidence: 0.9}
		offer := 0.2
		ans[jev.QOffer] = jev.Answer{Type: "noul", Noul: &offer}
		r := Result{Case: cs}
		r.judge(env, req, ans, ids)
		if r.Decision != "xhigh" || r.Mode != "ultracode" || r.FastPath != "" {
			t.Errorf("go after %q, %s 0.6: %+v", tc.last, tc.rel, r)
		}
	}
	// Staying on the detour (the assistant offered one more thing for it),
	// the go-ahead runs at its level, whatever the relation reads: the
	// offer may be more of the detour, which a sure wrap-up can't tell.
	for _, tc := range []struct {
		level string
		rel   map[string]float64
		want  string
	}{
		{"xhigh", map[string]float64{"continue": 0.8, "new_task": 0.2}, "medium"},
		{"xhigh", map[string]float64{"wrap_up": 0.92, "continue": 0.08}, "medium"},
		{"low", map[string]float64{"wrap_up": 0.92, "continue": 0.08}, "medium"},
		{"low", map[string]float64{"wrap_up": 0.49, "aside": 0.3, "continue": 0.21}, "medium"},
	} {
		cs := Case{Scope: catalog.ScopeMain, Warm: true, State: st("last_assistant", "Fixed. The same sleep is in the cart spec: want me to fix it there too?",
			"current", map[string]any{"tier": "medium"}, "work_in_progress", map[string]any{"goal": "fix the flaky checkout spec", "level": "medium"},
			"paused_work", map[string]any{"goal": "migrate the handlers", "level": "xhigh", "mode": "ultracode"})}
		cs.State["task"] = "yes"
		_, req := setup(c, cs)
		ans, ids := levelAnswer(c, tc.level)
		ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: tc.rel, Confidence: 0.5}
		offer, ultra := 0.9, 0.95
		ans[jev.QOffer] = jev.Answer{Type: "noul", Noul: &offer}
		ans[jev.QModePfx+"ultracode"] = jev.Answer{Type: "noul", Noul: &ultra}
		r := Result{Case: cs}
		r.judge(env, req, ans, ids)
		if r.Decision != tc.want || r.Mode != "" {
			t.Errorf("yes staying on a medium detour, level %s, %v: %+v", tc.level, tc.rel, r)
		}
	}
	// Once a wrap-up closed the work (no paused work), a go-ahead to a
	// proposal is asked the relation, and holds the work unless it is a
	// wrap-up step or an aside.
	for rel, want := range map[string]string{"new_task": "high", "continue": "high", "wrap_up": "low", "aside": "low"} {
		cs := Case{Scope: catalog.ScopeMain, Warm: true, State: st("task", "yes", "last_assistant", "Committed. The exporter has no such check: want me to add it there too?",
			"current", map[string]any{"tier": "high"}, "work_in_progress", map[string]any{"goal": "validate the CSV import", "level": "high", "done": true})}
		_, req := setup(c, cs)
		if g, b := proposalGoAhead(env, cs, req); !g || b || fastPath(env, cs, req) != "" {
			t.Errorf("yes to a proposal after a done work: go-ahead %v, back first %v", g, b)
		}
		ans, ids := levelAnswer(c, "low")
		ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: map[string]float64{rel: 0.9, "extend": 0.1}, Confidence: 0.9}
		r := Result{Case: cs}
		r.judge(env, req, ans, ids)
		if r.Decision != want {
			t.Errorf("yes to a proposal after a done work, %s: %+v", rel, r)
		}
	}
	// Typed mid-turn during the detour: it goes on with the turn.
	r = judge(Case{Scope: catalog.ScopeMain, Warm: true, State: st("mid_turn", true,
		"current", map[string]any{"tier": "low"}, "work_in_progress", map[string]any{"goal": "fix the README typo", "level": "low"},
		"paused_work", map[string]any{"goal": "migrate the handlers", "level": "xhigh", "mode": "ultracode"})})
	if r.Decision != "low" || r.Mode != "" {
		t.Errorf("go-ahead typed mid-turn during a detour: %+v", r)
	}
}

// Talking about an effort for something else, with Jev half-reading it as
// a request (effort_low 0.88, as live): below the work it needs 0.9, so
// the xhigh work stays, and the eval scores no request.
func TestEvalLowerEffortNeedsMore(t *testing.T) {
	c := testCatalog(t)
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	cs := Case{ID: "tuning", Scope: catalog.ScopeMain, Warm: true, Want: "xhigh", Relation: "extend", State: map[string]any{
		"phase": "warm", "task": "set it to low in the tuning file for the subagents, then keep going on the deadlock",
		"current": map[string]any{"tier": "xhigh"}, "work_in_progress": map[string]any{"goal": "find the scheduler deadlock", "level": "xhigh"}}}
	_, req := setup(c, cs)
	ans, ids := levelAnswer(c, "high")
	ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: map[string]float64{"extend": 0.9, "new_task": 0.1}, Confidence: 0.9}
	p := 0.88
	ans[jev.QExplicitPfx+"effort_low"] = jev.Answer{Type: "noul", Noul: &p}
	r := Result{Case: cs, ExplicitP: map[string]float64{"effort_low": p}}
	r.judge(env, req, ans, ids)
	if r.Decision != "xhigh" {
		t.Errorf("decision %s (%s)", r.Decision, r.Hold)
	}
	if e := ExplicitMetrics(c, []Result{r}); e.FP != 0 || e.TP != 0 {
		t.Errorf("explicit = %+v", e)
	}
}

// levelAnswer is Jev certain of level, as the router reads it.
func levelAnswer(c *catalog.Catalog, level string) (map[string]jev.Answer, []string) {
	_, ids := jev.Questions(c, catalog.ScopeMain, jev.Ask{})
	lv := jev.Answer{Type: "score", Probabilities: map[string]float64{}, Confidence: 1}
	for i, id := range ids {
		lv.Probabilities[fmt.Sprint(i)] = map[bool]float64{true: 1}[id == level]
	}
	return map[string]jev.Answer{jev.QLevel: lv}, ids
}

// Once a wrap-up closed the work, a question gets its own level: it holds
// nothing; more work on it does.
func TestDoneWorkHolds(t *testing.T) {
	done := map[string]any{"work_in_progress": map[string]any{"goal": "fix the race", "level": "xhigh", "done": true}}
	for rel, want := range map[string]bool{"side_question": false, "inform": false, "aside": false, "extend": true, "continue": true} {
		if got := (Case{Relation: rel, State: done}).holds(); got != want {
			t.Errorf("%s after a wrap-up: holds %v", rel, got)
		}
	}
	if !(Case{Relation: "side_question", State: map[string]any{}}).holds() || (Case{Relation: "aside", State: map[string]any{}}).holds() {
		t.Error("open work")
	}
}

// The held-out cases of the first fresh run (2026-09-30) the router got
// wrong, on the answers Jev gave then: a wrap-up or an aside in an
// ultracode session ran with the mode (Jev's mode answer reads the whole
// work); "looks good." on a done work was carried at its xhigh; "think
// harder" on low work went to xhigh, Jev rating the words; a model asked
// for the rest of medium work went to high.
func TestHeldOutRound3(t *testing.T) {
	c := testCatalog(t)
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	type answers struct {
		level  string
		rel    map[string]float64
		ultra  float64
		asks   map[string]float64
		spread map[string]float64 // the level's probabilities, when not sure
	}
	judge := func(cs Case, a answers) Result {
		t.Helper()
		_, req := setup(c, cs)
		ans, ids := levelAnswer(c, a.level)
		if a.spread != nil {
			lv, peak := ans[jev.QLevel], 0.0
			for i, id := range ids {
				lv.Probabilities[fmt.Sprint(i)] = a.spread[id]
				peak = max(peak, a.spread[id])
			}
			// Jev's confidence: (n·peak - 1)/(n - 1).
			n := float64(len(ids))
			lv.Confidence = (n*peak - 1) / (n - 1)
			ans[jev.QLevel] = lv
		}
		if a.rel != nil {
			ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: a.rel, Confidence: 0.9}
		}
		ans[jev.QModePfx+"ultracode"] = jev.Answer{Type: "noul", Noul: &a.ultra}
		r := Result{Case: cs, ExplicitP: a.asks}
		for k, p := range a.asks {
			ans[jev.QExplicitPfx+k] = jev.Answer{Type: "noul", Noul: &p}
		}
		r.judge(env, req, ans, ids)
		return r
	}
	sweep := map[string]any{"goal": "Migrate all 40 services from requests to httpx, as a workflow across the services", "level": "xhigh", "mode": "ultracode"}
	st := func(task string) map[string]any {
		return map[string]any{"phase": "warm", "task": task, "current": map[string]any{"tier": "xhigh", "mode": "ultracode"}, "work_in_progress": sweep}
	}
	for _, x := range []struct {
		task, rel, want string
	}{
		{"small unrelated question: what's the Python equivalent of flatMap?", "aside", "low"},
		{"commit wave 2, one commit per service, conventional commit messages", "wrap_up", "low"},
		{"how many services are left?", "side_question", "xhigh"},
	} {
		r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: st(x.task)}, answers{level: "low", rel: map[string]float64{x.rel: 0.9, "extend": 0.1}, ultra: 0.93})
		if r.Decision != x.want || r.Mode != "" {
			t.Errorf("%s in an ultracode session: %s +%q (%s)", x.rel, r.Decision, r.Mode, r.Hold)
		}
	}

	done := map[string]any{"phase": "warm", "task": "looks good.", "last_assistant": "CI is green on the PR.",
		"current": map[string]any{"tier": "low"}, "work_in_progress": map[string]any{"goal": "Fix the SSRF in the image proxy", "level": "xhigh", "done": true}}
	if r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: done}, answers{level: "low", rel: map[string]float64{"aside": 0.9, "continue": 0.1}}); r.Decision != "low" || r.FastPath != "" || r.Kept == "go-ahead" {
		t.Errorf("acknowledgement of a done work: %+v", r)
	}
	if r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: done}, answers{level: "low", rel: map[string]float64{"continue": 0.9, "aside": 0.1}}); r.Decision != "xhigh" {
		t.Errorf("go-ahead reopening a done work: %s (%s)", r.Decision, r.Hold)
	}

	cache := map[string]any{"phase": "warm", "task": "explain the touch chain again but think harder: why would a price change not refresh the card?",
		"current": map[string]any{"tier": "low"}, "work_in_progress": map[string]any{"goal": "How does cache invalidation work here? Just explain.", "level": "low"}}
	if r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: cache}, answers{spread: map[string]float64{"low": 0.55, "xhigh": 0.45},
		rel: map[string]float64{"extend": 0.8, "new_task": 0.2}, asks: map[string]float64{"effort_more": 0.97}}); r.Decision != "medium" {
		t.Errorf("think harder on low work: %s (%s)", r.Decision, r.Hold)
	}

	landing := map[string]any{"phase": "warm", "task": "fais le reste avec Sonnet, pas besoin d'Opus pour du CSS",
		"current": map[string]any{"tier": "medium"}, "work_in_progress": map[string]any{"goal": "Fais une landing page (Astro + Tailwind)", "level": "medium"}}
	if r := judge(Case{Scope: catalog.ScopeMain, Warm: true, State: landing}, answers{spread: map[string]float64{"medium": 0.54, "high": 0.46},
		rel: map[string]float64{"continue": 0.92, "extend": 0.08}, asks: map[string]float64{"model_claude-sonnet-5-5": 0.88}}); r.Decision != "medium" || r.Model != "claude-sonnet-5-5" {
		t.Errorf("model for the rest of the work: %s on %q (%s)", r.Decision, r.Model, r.Hold)
	}
}

// The eval records Jev's relation on the prompts the hooks take without
// it (a go-ahead), asked alone in a call of its own: the decision's
// questions stay the hooks', and neither the decision nor the relation
// metrics use it.
func TestRelationAskedApartOnFastPath(t *testing.T) {
	c := testCatalog(t)
	var mu sync.Mutex
	var calls []map[string]jev.Question
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jev.Request
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		calls = append(calls, req.Questions)
		mu.Unlock()
		out := map[string]any{}
		for id, q := range req.Questions {
			switch {
			case id == jev.QRelation:
				out[id] = map[string]any{"type": "choice", "choice": "continue", "confidence": 0.9, "probabilities": map[string]float64{"continue": 0.95, "extend": 0.05}}
			case q.Type == "score":
				out[id] = map[string]any{"type": "score", "confidence": 1, "probabilities": map[string]float64{"0": 1}}
			case q.Type == "choice":
				out[id] = map[string]any{"type": "choice", "choice": jev.ExplicitNone, "confidence": 1, "probabilities": map[string]float64{jev.ExplicitNone: 1}}
			default:
				out[id] = map[string]any{"type": "noul", "noul": 0.05}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": out, "usage": map[string]any{"cost": 0.0001}})
	}))
	defer srv.Close()
	env := &router.Env{Cfg: config.Default(), Catalog: c, Jev: &jev.Client{URL: srv.URL, APIKey: "k", HTTP: srv.Client()}}
	cs := Case{ID: "go", Scope: catalog.ScopeMain, Warm: true, Want: "xhigh", Relation: "continue", State: map[string]any{
		"phase": "warm", "task": "go on.", "current": map[string]any{"tier": "low"}, "work_in_progress": map[string]any{"goal": "fix the race", "level": "xhigh"}}}
	rs := Run(context.Background(), env, []Case{cs}, "score", 1, 1)
	r := rs[0]
	if len(calls) != 2 || calls[0][jev.QRelation].Type != "" || len(calls[1]) != 1 || calls[1][jev.QRelation].Type != "choice" {
		t.Fatalf("questions asked: %v", calls)
	}
	if r.FastPath != "go-ahead" || r.RelP["continue"] != 0.95 || r.Decision != "xhigh" || r.Kept != "go-ahead" {
		t.Errorf("result: %+v", r)
	}
	if s := RelationMetrics(rs); s.N != 0 {
		t.Errorf("relation metrics count the fast path: %+v", s)
	}
	if b, _ := json.Marshal(r); !strings.Contains(string(b), `"relation_p":{"continue":0.95`) || !strings.Contains(string(b), `"fast_path":"go-ahead"`) {
		t.Errorf("JSON = %s", b)
	}
	if r = Rejudge(env, rs)[0]; r.Decision != "xhigh" || r.FastPath != "go-ahead" {
		t.Errorf("rejudged: %+v", r)
	}
}

// explicitTier is the tier the request for effort e maps to ("" if none).
func explicitTier(req router.Request, e string) string {
	for _, x := range req.Explicit {
		if x.Kind == "effort" && x.Value == e {
			return x.Tier
		}
	}
	return ""
}

// Blind: when Jev sees nothing of what the work is about (no goal, no
// recent prompts, no compaction summary), a separate relation doesn't take
// the prompt below the work; with the work's goal, it does.
func TestBlindHolds(t *testing.T) {
	c := testCatalog(t)
	env := &router.Env{Cfg: config.Default(), Catalog: c}
	mk := func(st map[string]any) []Result {
		return []Result{{Case: Case{ID: "b", Scope: catalog.ScopeMain, Warm: true, Want: "high", State: st},
			Probs: map[string]float64{"low": 0.8, "high": 0.2}, Conf: 0.6, RelP: map[string]float64{"aside": 0.8, "side_question": 0.2}}}
	}
	blind := map[string]any{"phase": "warm", "task": "why does the status line show xhigh struck through?", "current": map[string]any{"tier": "high"},
		"last_assistant": "Nothing more to do: the test run already finished."}
	if r := Rejudge(env, mk(blind))[0]; r.Decision != "high" {
		t.Errorf("blind aside lowered the work: %s (%s)", r.Decision, r.Hold)
	}
	seen := map[string]any{"phase": "warm", "task": "unrelated: what does HTTP 409 mean?", "current": map[string]any{"tier": "high"},
		"work_in_progress": map[string]any{"goal": "render the routing fields in the status line", "level": "high"}}
	if r := Rejudge(env, mk(seen))[0]; r.Decision != "low" {
		t.Errorf("aside with a known work held: %s (%s)", r.Decision, r.Hold)
	}
}
