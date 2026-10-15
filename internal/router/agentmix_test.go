package router

import (
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/state"
)

// A fresh subagent stays on the model Jev's reading weighs most (live:
// sonnet-high 0.54 / opus-xhigh 0.24 / opus-high 0.22 went to opus-high
// once sonnet-xhigh left the scale: the 1.5 penalty on the Opus share
// outweighed Jev's Sonnet reading).
func TestAgentStaysOnJevModel(t *testing.T) {
	e := testEnv(t)
	rd := Reading{probs: map[string]float64{"sonnet-high": 0.54, "opus-xhigh": 0.24, "opus-high": 0.22}, conf: 0.54, top: "sonnet-high"}
	req := Request{Scope: catalog.ScopeSubagent}
	p := policy.Params{Penalty: 1.5, Scale: 1}
	e.Cfg.Features.AgentModelOnJevReading = false
	if got, _ := e.pick(req, rd, nil, p); got.Model != "claude-opus-5-5" {
		t.Skipf("the policy alone no longer goes to Opus here (%s)", got.ID)
	}
	e.Cfg.Features.AgentModelOnJevReading = true
	if got, _ := e.pick(req, rd, nil, p); got.ID != "sonnet-high" {
		t.Errorf("picked %s, want sonnet-high", got.ID)
	}
	// Opus holding most of the reading still goes to Opus.
	rd = Reading{probs: map[string]float64{"sonnet-high": 0.4, "opus-xhigh": 0.35, "opus-high": 0.25}, conf: 0.4, top: "sonnet-high"}
	if got, _ := e.pick(req, rd, nil, p); got.Model != "claude-opus-5-5" {
		t.Errorf("picked %s with 0.6 of the reading on Opus", got.ID)
	}
}

// An agent that stays on Opus because a switch wouldn't pay back its cache
// runs at the effort Jev's reading needs (live: agents moved from
// opus-xhigh stayed on opus-high for messages read as Sonnet medium work).
func TestAgentEffortStepDown(t *testing.T) {
	e := testEnv(t)
	cur := e.Catalog.Tier(catalog.ScopeSubagent, "opus-high")
	if cur == nil {
		t.Skip("no opus-high subagent tier")
	}
	rd := Reading{probs: map[string]float64{"sonnet-medium": 0.6, "sonnet-high": 0.3, "haiku": 0.1}, conf: 0.6, top: "sonnet-medium"}
	if got := e.agentEffort(rd, cur); got != "medium" {
		t.Errorf("effort %s, want medium", got)
	}
	rd = Reading{probs: map[string]float64{"opus-high": 0.6, "sonnet-high": 0.4}, conf: 0.6, top: "opus-high"}
	if got := e.agentEffort(rd, cur); got != "high" {
		t.Errorf("effort %s on an Opus reading, want high", got)
	}
	e.Cfg.Features.AgentEffortStepDown = false
	rd = Reading{probs: map[string]float64{"sonnet-medium": 1}, conf: 1, top: "sonnet-medium"}
	if got := e.agentEffort(rd, cur); got != "high" {
		t.Errorf("effort %s with the switch off, want high", got)
	}
}

// A resumed agent's model moves symmetrically (live, 2026-10-15: me-lab went
// sonnet-medium → opus-xhigh on one reading of Opus 0.54, then stayed on Opus
// for readings of 0.15 and 0.22 because the rewrite back didn't pay back
// within three messages).
func TestAgentModelHysteresis(t *testing.T) {
	e := testEnv(t)
	tier := func(id string) *catalog.Tier { return e.Catalog.Tier(catalog.ScopeSubagent, id) }
	rewrite := func(cur *catalog.Tier) func(*catalog.Tier) float64 {
		return func(t *catalog.Tier) float64 {
			switch {
			case t.Model == cur.Model:
				return 0 // per-turn effort keeps the cache
			case t.Model == "claude-opus-5-5":
				return 940_000 * (8 - 0.1) / 1e6
			}
			return 940_000 * (4 - 0.2) / 1e6
		}
	}
	warm := func(cur *catalog.Tier, prev map[string]float64) Request {
		return Request{Scope: catalog.ScopeSubagent, Warm: true, SwitchCost: rewrite(cur), PrevProbs: prev,
			Current: &state.Decision{Scope: catalog.ScopeSubagent, Tier: cur.ID, Model: cur.Model, Effort: cur.Effort}}
	}
	p := policy.Params{Penalty: 1.5, Scale: 1}
	// The live agent's turns cost about ten times its tier's catalog cost.
	pUp := policy.Params{Penalty: 1.5, Scale: 30}
	// Over three messages, rewriting 940K for Sonnet doesn't pay back; over
	// the sixteen an agent still gets, it does.
	pDown := policy.Params{Penalty: 1.5, Scale: 3}

	// Up: one reading of Opus 0.54 after a Sonnet one doesn't move it.
	son := tier("sonnet-medium")
	up := Reading{probs: map[string]float64{"opus-high": 0.01, "opus-xhigh": 0.53, "sonnet-high": 0.25, "sonnet-medium": 0.21}, conf: 0.53, top: "opus-xhigh"}
	lowPrev := map[string]float64{"sonnet-medium": 0.8, "sonnet-high": 0.2}
	e.Cfg.Features.AgentModelHysteresis = false
	if got, _ := e.pick(warm(son, lowPrev), up, son, pUp); got.Model != "claude-opus-5-5" {
		t.Skipf("the policy alone no longer goes to Opus here (%s)", got.ID)
	}
	e.Cfg.Features.AgentModelHysteresis = true
	if got, _ := e.pick(warm(son, lowPrev), up, son, pUp); got.Model == "claude-opus-5-5" {
		t.Errorf("went to %s on one 0.54 reading", got.ID)
	}
	// Sustained (half of this reading and of the previous) or strong: Opus.
	if got, _ := e.pick(warm(son, map[string]float64{"opus-xhigh": 0.6, "sonnet-high": 0.4}), up, son, pUp); got.Model != "claude-opus-5-5" {
		t.Errorf("stayed on %s on a sustained Opus reading", got.ID)
	}
	strong := Reading{probs: map[string]float64{"opus-xhigh": 0.75, "sonnet-high": 0.25}, conf: 0.75, top: "opus-xhigh"}
	if got, _ := e.pick(warm(son, nil), strong, son, pUp); got.Model != "claude-opus-5-5" {
		t.Errorf("stayed on %s on Opus 0.75", got.ID)
	}

	// Down: Opus at 0.15 of the reading leaves Opus, though rewriting 940K
	// doesn't pay back on the next messages.
	op := tier("opus-xhigh")
	down := Reading{probs: map[string]float64{"haiku": 0.09, "opus-high": 0.05, "opus-xhigh": 0.1, "sonnet-high": 0.21, "sonnet-medium": 0.55}, conf: 0.55, top: "sonnet-medium"}
	e.Cfg.Features.AgentModelHysteresis = false
	if v := e.Judge(warm(op, nil), down, op, policy.RepoPolicy{}, pDown); v.Tier.Model != "claude-opus-5-5" {
		t.Errorf("the case doesn't reproduce: left Opus for %s without the feature", v.Tier.ID)
	}
	e.Cfg.Features.AgentModelHysteresis = true
	v := e.Judge(warm(op, nil), down, op, policy.RepoPolicy{}, pDown)
	if v.Tier.Model == "claude-opus-5-5" {
		t.Errorf("stayed on %s (hold %q) on Opus 0.15", v.Tier.ID, v.Hold)
	}
	if e.downConfidence(down, catalog.ScopeSubagent, v.Tier) < e.Cfg.Features.WarmMinConfidence {
		t.Errorf("left for %s, not enough on Jev's reading", v.Tier.ID)
	}
	// Opus at 0.3: not sure enough, the rewrite decides as before.
	mid := Reading{probs: map[string]float64{"opus-xhigh": 0.3, "sonnet-medium": 0.7}, conf: 0.7, top: "sonnet-medium"}
	e.Cfg.Features.AgentModelHysteresis = false
	before := e.Judge(warm(op, nil), mid, op, policy.RepoPolicy{}, p)
	e.Cfg.Features.AgentModelHysteresis = true
	if after := e.Judge(warm(op, nil), mid, op, policy.RepoPolicy{}, p); after.Tier.ID != before.Tier.ID {
		t.Errorf("Opus 0.3: %s, was %s", after.Tier.ID, before.Tier.ID)
	}
}
