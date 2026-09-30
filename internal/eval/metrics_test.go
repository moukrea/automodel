package eval

import (
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
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
			m[r] = (1 - p) / 5
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
	}
	for i := range rs {
		rs[i].Accept, rs[i].Probs = []string{rs[i].Want}, map[string]float64{rs[i].Got: 1}
	}
	s := Summarize(c, rs)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if r := s.Relation; r.N != 3 || r.Right != 2 || r.Confusion["side_question"]["wrap_up"] != 1 || !near(r.ECE, (2*0.075+0.7)/3) {
		t.Errorf("relation %+v", r)
	}
	if e := s.Explicit; e.TP != 1 || e.FP != 1 || e.FN != 2 || !near(e.Precision(), 0.5) || !near(e.Recall(), 1.0/3) {
		t.Errorf("explicit %+v", e)
	}
	if f := s.Follow; f.N != 2 || f.Right != 1 {
		t.Errorf("follow-ups %+v", f)
	}
	if m := s.ModeDecision["ultracode"]; m == nil || m.N != 1 || m.Right != 0 {
		t.Errorf("mode decisions %+v", m)
	}
	st := s.Scopes[main]
	if st.FollowUnder != 1 {
		t.Errorf("below the label on follow-ups: %d", st.FollowUnder)
	}
	if fails := strings.Join(st.Check(DefaultGate), "; "); !strings.Contains(fails, "1 decisions below the label on follow-ups") {
		t.Errorf("gate: %s", fails)
	}
	var b strings.Builder
	PrintSummary(&b, s)
	for _, want := range []string{"relation: 2/3 right (67%)", "| side_question | 0 | 0 | 0 | 0 | 1 | 0 |", "explicit requests: precision 50%, recall 33%",
		"1/2 decisions below the work in progress", "mode ultracode on/off (router decision): 0/1 right"} {
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
