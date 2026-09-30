package router

import (
	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/state"
)

// Spending cap ([budget]): once the day's or the session's spend reaches
// its cap, routing picks no tier above budget.max_tier_when_over until the
// next day or session. A user's pin is not capped: it's their call.

// OverBudget reports whether a cap is reached for a session (nil: the day only).
func (e *Env) OverBudget(s *state.Session) bool {
	b := e.Cfg.Budget
	if b.USDPerDay > 0 && e.State.SpentToday(e.Now()) >= b.USDPerDay {
		return true
	}
	return b.USDPerSession > 0 && s != nil && s.TotalUSD >= b.USDPerSession
}

// BudgetCap is the highest tier a scope may get now, or nil when no cap applies.
func (e *Env) BudgetCap(sessionID, scope string) *catalog.Tier {
	b := e.Cfg.Budget
	if b.USDPerDay <= 0 && b.USDPerSession <= 0 {
		return nil
	}
	s, _ := e.State.Load(sessionID)
	if !e.OverBudget(s) {
		return nil
	}
	id := b.MaxTierWhenOver
	if scope == catalog.ScopeSubagent {
		id = b.MaxSubagentTierWhenOver
	}
	return e.Catalog.Tier(scope, id)
}

// AboveCap reports whether t is above the scope's budget cap.
func (e *Env) AboveCap(sessionID, scope string, t *catalog.Tier) bool {
	lim := e.BudgetCap(sessionID, scope)
	return lim != nil && t != nil && t.Rank > lim.Rank
}

// capBudget lowers a verdict to the budget cap (applied after Judge), and
// returns the tier it would have been ("" when not capped).
func (e *Env) capBudget(req Request, v Verdict, cur *catalog.Tier) (Verdict, string) {
	lim := e.BudgetCap(req.SessionID, req.Scope)
	if lim == nil || v.Tier.Rank <= lim.Rank {
		return v, ""
	}
	from := v.Tier.ID
	v.Tier, v.Keep = lim, ""
	for _, m := range e.Catalog.ModesFor(req.Scope) {
		if min := e.Catalog.Tier(req.Scope, m.MinTier); m.ID == v.Mode && min != nil && lim.Rank < min.Rank {
			v.Mode = ""
		}
	}
	if cur != nil && cur.ID == lim.ID && v.Mode == req.Current.Mode {
		v.Keep = "budget cap"
	}
	return v, from
}

// capsMode reports a budget cap below the tier a mode needs: the mode
// stays off until the cap lifts.
func (e *Env) capsMode(req Request, mode string) bool {
	lim, m := e.BudgetCap(req.SessionID, req.Scope), e.Catalog.Modes[mode]
	if lim == nil || m == nil {
		return false
	}
	min := e.Catalog.Tier(req.Scope, m.MinTier)
	return min != nil && lim.Rank < min.Rank
}

// capTier is capBudget for a tier alone (fallbacks).
func (e *Env) capTier(req Request, t *catalog.Tier) *catalog.Tier {
	if lim := e.BudgetCap(req.SessionID, req.Scope); lim != nil && t.Rank > lim.Rank {
		return lim
	}
	return t
}

// bounded is a tier and mode decided without Jev (a fallback, a go-ahead)
// within the repo's bounds and the budget cap: the mode is dropped when
// the repo disables it or the tier ends up below its min_tier.
func (e *Env) bounded(req Request, t *catalog.Tier, mode string, rp policy.RepoPolicy) (*catalog.Tier, string) {
	c := e.Catalog
	t = e.capTier(req, policy.Constrain(c, req.Scope, t, rp, req.Context))
	md := c.Modes[mode]
	if md == nil || !rp.ModeAllowed(mode) {
		return t, ""
	}
	if min := c.Tier(req.Scope, md.MinTier); min != nil && t.Rank < min.Rank {
		return t, ""
	}
	return t, mode
}
