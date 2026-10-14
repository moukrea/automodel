package router

import (
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/policy"
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
