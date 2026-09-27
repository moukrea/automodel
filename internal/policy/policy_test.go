package policy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
)

func cat(t *testing.T) *catalog.Catalog {
	c, is, err := catalog.Load("../../catalog.toml", time.Now(), 3650)
	if err != nil || len(is.Errors()) > 0 {
		t.Fatal(err, is)
	}
	return c
}

func TestChoose(t *testing.T) {
	c := cat(t)
	th := Thresholds{Act: 0.6, Low: 0.35}
	cases := []struct {
		name   string
		answer jev.Answer
		want   string
	}{
		{"confident", jev.Answer{Choice: "medium", Confidence: 0.8, Probabilities: map[string]float64{"medium": 0.8, "high": 0.1}}, "medium"},
		{"unsure: take the higher of top two", jev.Answer{Choice: "medium", Confidence: 0.5, Probabilities: map[string]float64{"medium": 0.5, "high": 0.3, "low": 0.2}}, "high"},
		{"unsure, second is lower", jev.Answer{Choice: "high", Confidence: 0.5, Probabilities: map[string]float64{"high": 0.5, "low": 0.3}}, "high"},
		{"very unsure: one more rank", jev.Answer{Choice: "medium", Confidence: 0.3, Probabilities: map[string]float64{"medium": 0.3, "high": 0.28, "low": 0.2}}, "xhigh"},
		{"nothing above max", jev.Answer{Choice: "max", Confidence: 0.3, Probabilities: map[string]float64{"max": 0.3, "xhigh": 0.29}}, "max"},
	}
	for _, tc := range cases {
		if got := Choose(c, catalog.ScopeMain, &tc.answer, th).ID; got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestConstrain(t *testing.T) {
	c := cat(t)
	rp := RepoPolicy{MinTier: "high", MaxTier: "xhigh", MinSubagentTier: "opus-low"}
	if got := Constrain(c, "main", c.Tier("main", "low"), rp, 0).ID; got != "high" {
		t.Errorf("floor: %s", got)
	}
	if got := Constrain(c, "main", c.Tier("main", "max"), rp, 0).ID; got != "xhigh" {
		t.Errorf("ceiling: %s", got)
	}
	// Haiku's 200K window can't hold a 300K context.
	if got := Constrain(c, "subagent", c.Tier("subagent", "haiku"), RepoPolicy{}, 300_000).ID; got != "opus-low" {
		t.Errorf("context: %s", got)
	}
	if got := Constrain(c, "subagent", c.Tier("subagent", "haiku"), rp, 0).ID; got != "opus-low" {
		t.Errorf("subagent floor: %s", got)
	}
}

func TestLoadRepoPolicy(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	os.WriteFile(filepath.Join(root, ".automodel.toml"), []byte(`min_tier = "high"`), 0o644)
	sub := filepath.Join(root, "a", "b")
	os.MkdirAll(sub, 0o755)
	if p := LoadRepoPolicy(sub, ".automodel.toml"); p.MinTier != "high" {
		t.Errorf("got %+v", p)
	}
}

func TestCosts(t *testing.T) {
	c := cat(t)
	m := Costs(c, catalog.ScopeMain)
	if m["low"] != 0.55 || m["xhigh"] != 3.46 {
		t.Errorf("measured costs: %v", m)
	}
	s := Costs(c, catalog.ScopeSubagent)
	if s["haiku"] != 0.14 || s["opus-low"] != 0.55 {
		t.Errorf("subagent costs: %v", s)
	}
}

func TestBest(t *testing.T) {
	c := cat(t)
	p := Params{Penalty: 3, Scale: 1}
	// Split between low and medium, some mass higher: underprovisioning is
	// dearer than overprovisioning, so it settles on medium.
	probs := map[string]float64{"low": 0.65, "medium": 0.18, "high": 0.06, "xhigh": 0.11}
	if got := Best(c, "main", probs, nil, nil, p).Tier.ID; got != "medium" {
		t.Errorf("uncertain: %s", got)
	}
	if got := Best(c, "main", map[string]float64{"low": 0.97, "medium": 0.03}, nil, nil, p).Tier.ID; got != "low" {
		t.Errorf("confident low: %s", got)
	}
	// From xhigh, a free switch to low is taken; a $10 one is not.
	cur := c.Tier("main", "xhigh")
	low := map[string]float64{"low": 0.97, "medium": 0.03}
	free := func(*catalog.Tier) float64 { return 0 }
	dear := func(*catalog.Tier) float64 { return 10 }
	if pk := Best(c, "main", low, cur, free, p); pk.Tier.ID != "low" || pk.Gain <= 0 {
		t.Errorf("free switch: %+v", pk)
	}
	if pk := Best(c, "main", low, cur, dear, p); pk.Tier.ID != "xhigh" {
		t.Errorf("dear switch: %+v", pk)
	}
	// MaxGain: nothing can pay back $10 on a $0.1-per-unit horizon.
	if g := MaxGain(c, "main", cur, dear, Params{Penalty: 3, Scale: 0.1}); g > 0 {
		t.Errorf("max gain = %v", g)
	}
	if g := MaxGain(c, "main", cur, free, p); g <= 0 {
		t.Errorf("free max gain = %v", g)
	}
}
