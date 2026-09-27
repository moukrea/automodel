package router

import (
	"log"

	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/ledger"
)

func statesFor(cfg *config.Config) ledger.States {
	if !cfg.RecordStates {
		return ledger.States{}
	}
	return ledger.States{Dir: cfg.StateDir}
}

// keepState stores the routing state a decision sends to Jev (already
// metadata-only under privacy = "metadata"), keyed by the decision's ID.
func (e *Env) keepState(rec ledger.Decision, st map[string]any) {
	r := ledger.StateRecord{ID: rec.ID, TS: rec.TS, SessionID: rec.SessionID, Scope: rec.Scope, Warm: rec.Warm, State: st}
	if err := e.States.Put(r); err != nil {
		log.Printf("states: %v", err)
	}
}
