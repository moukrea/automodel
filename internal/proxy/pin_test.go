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

func ledgerDecisions(p *Proxy) ([]ledger.Decision, error) { return ledger.Decisions(p.Ledger.Path) }
