package proxy

import (
	"encoding/json"
	"log"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/ledger"
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
// first effort is its default; a change to another one means the user ran
// /effort, and a change back to the default releases the pin. Only changes
// count: a pin released by [effort:auto] stays released while Claude Code
// keeps sending the same effort. The pinned effort applies from the current
// turn, through the per-turn machinery when the model has it (the cache
// survives). The last effort seen is kept in memory, so requests that don't
// change it cost no state write.
func (p *Proxy) observeClientEffort(cat *catalog.Catalog, sessionID, effort string) {
	if sessionID == "" || effort == "" {
		return
	}
	p.mu.Lock()
	if p.clientEffort == nil {
		p.clientEffort = map[string]string{}
	}
	if p.clientEffort[sessionID] == effort {
		p.mu.Unlock()
		return
	}
	p.clientEffort[sessionID] = effort
	p.mu.Unlock()

	var pinned *state.Decision
	_, err := p.State.Update(sessionID, func(s *state.Session) bool {
		switch {
		case s.ClientEffort0 == "":
			s.ClientEffort0, s.ClientEffortLast = effort, effort
			return true
		case effort == s.ClientEffortLast:
			return false // not a change (the proxy restarted)
		}
		s.ClientEffortLast = effort
		if effort == s.ClientEffort0 && s.PinModel == "" {
			if s.PinSource == "/effort" {
				s.Pin, s.PinSource = "", ""
				log.Printf("pin %s: released (/effort back to %s)", short(sessionID), effort)
			}
			return true
		}
		if s.Main == nil {
			return true
		}
		if s.PinModel != "" { // pinned model: /effort changes its effort
			if m := cat.Model(s.PinModel); m != nil && m.SupportsEffort(effort) && s.Main.Effort != effort {
				d := *s.Main
				d.Effort, d.Cause, d.DecidedAt = effort, "/effort", time.Now()
				s.Main, s.Pin, s.PinSource = &d, effort, "/effort"
				if perTurn(cat, m) && !s.PerTurnRejected && s.EffortBase != "" {
					s.PendingEffort = &state.PendingEffort{Effort: effort, CreatedAt: time.Now()}
				}
				pinned = &d
			}
			return true
		}
		t := cat.TierFor(catalog.ScopeMain, s.Main.Model, effort)
		if t == nil {
			return true // an effort this model doesn't have (or not a main tier)
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
			pinned = &d
		}
		log.Printf("pin %s: %s (/effort)", short(sessionID), effort)
		return true
	})
	if err != nil {
		log.Printf("state %s: %v", short(sessionID), err)
		return
	}
	if pinned != nil {
		rec := ledger.Decision{TS: pinned.DecidedAt, Kind: "decision", SessionID: sessionID, Scope: catalog.ScopeMain,
			Trigger: "pinned", Cause: "/effort", Chosen: pinned.Tier, Model: pinned.APIID, Effort: pinned.Effort, Confidence: 1}
		if err := p.Ledger.Append(rec); err != nil {
			log.Printf("ledger: %v", err)
		}
	}
}
