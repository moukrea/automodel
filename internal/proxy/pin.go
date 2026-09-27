package proxy

import (
	"encoding/json"
	"log"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/state"
)

// clientEffort is the effort Claude Code asks for: its top-level
// output_config.effort, which follows /effort (checked on a live session).
// Effort-only messages in the history are not read: they may be past
// changes, and a false pin would stop routing.
func clientEffort(fields map[string]json.RawMessage) string {
	var oc struct {
		Effort string `json:"effort"`
	}
	if json.Unmarshal(fields["output_config"], &oc) == nil {
		return oc.Effort
	}
	return ""
}

// observeClientEffort turns a /effort change into a pin. Claude Code's
// first effort is its default: a different one means the user ran /effort,
// and going back to the default releases the pin. The pinned effort applies
// from the current turn, through the per-turn machinery when the model has
// it (the cache survives).
func (p *Proxy) observeClientEffort(cat *catalog.Catalog, sessionID, effort string) {
	if sessionID == "" || effort == "" {
		return
	}
	_, err := p.State.Update(sessionID, func(s *state.Session) bool {
		if s.ClientEffort0 == "" {
			s.ClientEffort0 = effort
			return true
		}
		switch {
		case effort == s.ClientEffort0 && s.PinSource == "/effort":
			s.Pin, s.PinSource = "", ""
			log.Printf("pin %s: released (/effort back to %s)", short(sessionID), effort)
			return true
		case effort == s.ClientEffort0, effort == s.Pin, s.Main == nil:
			return false
		}
		t := cat.TierFor(catalog.ScopeMain, s.Main.Model, effort)
		if t == nil {
			return false // an effort this model doesn't have (or not a main tier)
		}
		s.Pin, s.PinSource = effort, "/effort"
		if s.Main.Tier != t.ID {
			d := *s.Main
			d.Tier, d.Effort, d.Trigger, d.Cause, d.Mode, d.Workflows = t.ID, t.Effort, "pinned", "/effort", "", false
			d.Confidence, d.DecidedAt, d.Epoch = 1, time.Now(), s.Main.Epoch+1
			s.Main = &d
			if m := cat.Model(d.Model); m != nil && perTurn(cat, m) && !s.PerTurnRejected && s.EffortBase != "" {
				s.PendingEffort = &state.PendingEffort{Effort: d.Effort, CreatedAt: time.Now()}
			}
		}
		log.Printf("pin %s: %s (/effort)", short(sessionID), effort)
		return true
	})
	if err != nil {
		log.Printf("state %s: %v", short(sessionID), err)
	}
}
