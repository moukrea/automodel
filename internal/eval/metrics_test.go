package eval

import (
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
