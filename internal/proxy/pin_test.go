package proxy

import (
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/state"
)

func TestClientEffortPins(t *testing.T) {
	p, _, _ := setup(t)
	cat, err := p.Catalog.Get()
	if err != nil {
		t.Fatal(err)
	}
	p.State.Update("s", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "low", Model: "claude-opus-5-5", Effort: "low"}
		s.EffortBase = "low"
		return true
	})
	get := func() *state.Session { s, _ := p.State.Load("s"); return s }

	p.observeClientEffort(cat, "s", "xhigh") // Claude Code's default
	if s := get(); s.ClientEffort0 != "xhigh" || s.Pin != "" {
		t.Fatalf("first effort must only be recorded: %+v", s)
	}
	p.observeClientEffort(cat, "s", "xhigh")
	if get().Pin != "" {
		t.Fatal("the default effort pinned")
	}
	p.observeClientEffort(cat, "s", "high") // the user ran /effort high
	s := get()
	if s.Pin != "high" || s.PinSource != "/effort" || s.Main.Tier != "high" || s.PendingEffort == nil || s.PendingEffort.Effort != "high" {
		t.Fatalf("/effort not pinned through per-turn effort: %+v %+v", s, s.Main)
	}
	// [effort:auto] (the hook) releases it; Claude Code keeps sending high:
	// not a change, so no pin again.
	p.State.Update("s", func(s *state.Session) bool { s.Pin, s.PinSource = "", ""; return true })
	p.observeClientEffort(cat, "s", "high")
	if get().Pin != "" {
		t.Fatal("a released pin came back without a new /effort")
	}
	if ds, _ := ledgerDecisions(p); len(ds) != 1 || ds[0].Trigger != "pinned" || ds[0].Cause != "/effort" {
		t.Fatalf("/effort pin not logged: %+v", ds)
	}
	p.observeClientEffort(cat, "s", "medium") // a new /effort: pinned again
	if get().Pin != "medium" {
		t.Fatal("a new /effort didn't pin")
	}
	p.observeClientEffort(cat, "s", "ultracode") // not an effort of the model
	if get().Pin != "medium" {
		t.Fatal("an unknown effort changed the pin")
	}
	p.observeClientEffort(cat, "s", "xhigh") // back to the default: released
	if s := get(); s.Pin != "" || s.Main.Tier != "medium" {
		t.Fatalf("pin not released (the tier stays until routing resumes): %+v", s)
	}
	if cat.TierFor(catalog.ScopeMain, "claude-opus-5-5", "high") == nil {
		t.Fatal("catalog lookup")
	}
}

// /effort keeps the ultracode mode unless the effort is below the mode's.
func TestClientEffortPinKeepsTheMode(t *testing.T) {
	p, _, _ := setup(t)
	cat, _ := p.Catalog.Get()
	p.State.Update("u", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh", Mode: "ultracode", Workflows: true}
		return true
	})
	get := func() *state.Decision { s, _ := p.State.Load("u"); return s.Main }
	p.observeClientEffort(cat, "u", "medium") // Claude Code's default
	p.observeClientEffort(cat, "u", "max")
	if d := get(); d.Tier != "max" || d.Mode != "ultracode" || !d.Workflows {
		t.Fatalf("/effort max: %+v", d)
	}
	p.observeClientEffort(cat, "u", "high")
	if d := get(); d.Tier != "high" || d.Mode != "" || d.Workflows {
		t.Fatalf("/effort high: %+v", d)
	}
}

// /effort on the model a work asked for in words pins that model at that
// effort: a model outside the tiers has no tier to pin.
func TestClientEffortPinsTheWorkModel(t *testing.T) {
	p, _, _ := setup(t)
	cat, _ := p.Catalog.Get()
	p.State.Update("w", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: state.PinnedTier, Model: "claude-sonnet-5-5", APIID: "claude-sonnet-5-5", Effort: "high"}
		s.Work = &state.Work{Tier: "high", Model: "claude-sonnet-5-5"}
		return true
	})
	p.observeClientEffort(cat, "w", "medium") // Claude Code's default
	p.observeClientEffort(cat, "w", "xhigh")
	if s, _ := p.State.Load("w"); s.Pin != "xhigh" || s.PinModel != "claude-sonnet-5-5" || s.Main.Model != "claude-sonnet-5-5" || s.Main.Effort != "xhigh" {
		t.Fatalf("/effort xhigh on the work's model: pin %q/%q, %+v", s.Pin, s.PinModel, s.Main)
	}
}

// /effort below the mode's own effort on the work's model drops the mode,
// as on the tiers.
func TestClientEffortOnTheWorkModelKeepsModeRule(t *testing.T) {
	p, _, _ := setup(t)
	cat, _ := p.Catalog.Get()
	p.State.Update("w", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: state.PinnedTier, Model: "claude-sonnet-5-5", APIID: "claude-sonnet-5-5", Effort: "xhigh", Mode: "ultracode", Workflows: true}
		s.Work = &state.Work{Tier: "xhigh", Mode: "ultracode", Model: "claude-sonnet-5-5"}
		return true
	})
	p.observeClientEffort(cat, "w", "medium") // Claude Code's default
	p.observeClientEffort(cat, "w", "low")
	if s, _ := p.State.Load("w"); s.Main.Effort != "low" || s.Main.Mode != "" || s.Main.Workflows || s.PinModel != "claude-sonnet-5-5" {
		t.Fatalf("/effort low on the work's model: %+v", s.Main)
	}
}

func ledgerDecisions(p *Proxy) ([]ledger.Decision, error) { return ledger.Decisions(p.Ledger.Path) }

func TestPinnedModelRewrite(t *testing.T) {
	p, up, ps := setup(t)
	p.State.Update("sess-t", func(s *state.Session) bool {
		s.Main = &state.Decision{Scope: "main", Tier: state.PinnedTier, Model: "claude-sonnet-5", APIID: "claude-sonnet-5", Effort: "high", Trigger: "pinned"}
		s.PinModel, s.Pin = "claude-sonnet-5", "high"
		return true
	})
	post(t, ps.URL, map[string]string{HeaderSession: "sess-t"}, turnBody(user("p1", true)))
	m := up.last(t)
	if m["model"] != "claude-sonnet-5" || m["output_config"].(map[string]any)["effort"] != "high" {
		t.Fatalf("pinned model not applied: model %v, output_config %v", m["model"], m["output_config"])
	}
}
