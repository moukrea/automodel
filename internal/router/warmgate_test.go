package router

import (
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/state"
)

// A costly move down goes only to a tier Jev's reading says is enough, not
// to the one whose cache rewrite is cheapest (live: a 620K teammate read
// sonnet-high 0.88 / sonnet-xhigh 0.11 / haiku 0.01 left opus-xhigh for
// Haiku, its rewrite under the ×5 long-prompt price costing less than
// Sonnet's, and ran 70 requests of a game port there).
func TestWarmDownGoesWhereJevReads(t *testing.T) {
	e := testEnv(t)
	cur := e.Catalog.Tier(catalog.ScopeSubagent, "opus-xhigh")
	rd := Reading{probs: map[string]float64{"sonnet-high": 0.88, "sonnet-xhigh": 0.11, "haiku": 0.01}, conf: 0.88, top: "sonnet-high"}
	sc := func(t *catalog.Tier) float64 {
		switch {
		case t.ID == cur.ID:
			return 0
		case t.Model == "claude-haiku-5-5":
			return 0.496 // 620K × (1.00 − 0.20) / 1e6
		case t.Model == cur.Model:
			return 620_000 * (8 - 0.2) / 1e6
		}
		return 620_000 * (4 - 0.2) / 1e6
	}
	req := Request{Scope: catalog.ScopeSubagent, Warm: true, Current: &state.Decision{Scope: catalog.ScopeSubagent, Tier: cur.ID, Model: cur.Model, Effort: cur.Effort}, SwitchCost: sc}
	p := policy.Params{Penalty: 1.5, Scale: 1.5}
	if got, _ := e.pick(req, rd, cur, p); got.ID != "haiku" {
		t.Skipf("the policy alone no longer picks haiku here (%s): the case doesn't reproduce", got.ID)
	}
	v := e.Judge(req, rd, cur, policy.RepoPolicy{}, p)
	if v.Tier.ID != "sonnet-high" {
		t.Errorf("tier %s (hold %q), want sonnet-high: Haiku read 0.01 must not pass on sonnet-high's confidence", v.Tier.ID, v.Hold)
	}
	if v.Pick == nil || v.Pick.Tier.ID != "sonnet-high" || v.Pick.Gain <= 0 {
		t.Errorf("pick recorded %+v", v.Pick)
	}
	// Nothing enough below pays back: stay.
	rd = Reading{probs: map[string]float64{"opus-xhigh": 0.5, "haiku": 0.5}, conf: 0.5, top: "opus-xhigh"}
	if v := e.Judge(req, rd, cur, policy.RepoPolicy{}, p); v.Tier.ID != cur.ID && e.downConfidence(rd, req.Scope, v.Tier) < e.Cfg.Features.WarmMinConfidence {
		t.Errorf("moved to %s on %.2f", v.Tier.ID, e.downConfidence(rd, req.Scope, v.Tier))
	}
}

// Jev's confidence stands for the tier it names, not for a lower one.
func TestDownConfidenceIsTheTargets(t *testing.T) {
	e := testEnv(t)
	rd := Reading{probs: map[string]float64{"sonnet-high": 0.88, "sonnet-xhigh": 0.11, "haiku": 0.01}, conf: 0.91, top: "sonnet-high"}
	if got := e.downConfidence(rd, catalog.ScopeSubagent, e.Catalog.Tier(catalog.ScopeSubagent, "haiku")); got > 0.02 {
		t.Errorf("haiku: %.2f, want 0.01", got)
	}
	if got := e.downConfidence(rd, catalog.ScopeSubagent, e.Catalog.Tier(catalog.ScopeSubagent, "sonnet-high")); got < 0.91 {
		t.Errorf("sonnet-high: %.2f, want Jev's 0.91", got)
	}
}
