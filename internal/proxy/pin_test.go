package proxy

import (
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
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
	p.observeClientEffort(cat, "s", "ultracode") // not an effort of the model
	if get().Pin != "high" {
		t.Fatal("an unknown effort changed the pin")
	}
	p.observeClientEffort(cat, "s", "xhigh") // back to the default: released
	if s := get(); s.Pin != "" || s.Main.Tier != "high" {
		t.Fatalf("pin not released (the tier stays until routing resumes): %+v", s)
	}
	if cat.TierFor(catalog.ScopeMain, "claude-opus-5-5", "high") == nil {
		t.Fatal("catalog lookup")
	}
}
