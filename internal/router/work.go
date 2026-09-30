package router

import (
	"fmt"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/state"
)

// The work in progress (state.Work) is what a follow-up is measured
// against. Jev's level question rates the new prompt's own words, so "also
// add a test for that" after a race fix reads as medium work; the effort,
// though, governs the whole turn, which carries the pending work on. A
// prompt that follows the work up (a go-ahead, an addition, a fact, a side
// question) keeps at least the work's tier; only separate work (a new
// task, a wrap-up) gets its own level. Upgrades are always free: a low
// session given hard work goes up at once.

// WorkUpdate is what a decision makes of the session's work in progress.
type WorkUpdate struct {
	Kind string // WorkNew, WorkSet or WorkRaised
	Tier string
	Mode string
}

const (
	WorkNew    = "new"    // the prompt starts it: tier, mode and goal from this turn
	WorkSet    = "set"    // the user set its effort or mode in words
	WorkRaised = "raised" // a follow-up needed more
)

// Apply records the update on the session (prompt: the one decided on).
func (u *WorkUpdate) Apply(s *state.Session, prompt string, now time.Time) {
	if u == nil {
		return
	}
	w := state.Work{Tier: u.Tier, Mode: u.Mode, Since: now}
	if prev := s.WorkInProgress(); prev != nil && u.Kind != WorkNew {
		w.Goal, w.Since = prev.Goal, prev.Since
	} else {
		w.Goal = head(state.NormalizePrompt(prompt), state.WorkGoalChars)
	}
	s.Work = &w
}

// WorkLevel is how the state names the work's level to Jev: its effort,
// or the tier's ID for a tier without one.
func WorkLevel(c *catalog.Catalog, tier string) string {
	t := c.Tier(catalog.ScopeMain, tier)
	switch {
	case t == nil:
		return ""
	case t.Effort != "":
		return t.Effort
	}
	return t.ID
}

// holdAt says whether the prompt follows the work in progress up, and then
// the tier the decision can't go below and why. Prompts that arrive while
// Claude works, and messages from other sessions, never lower the effort
// the turn runs at either.
func (e *Env) holdAt(req Request, rd Reading, work, cur *catalog.Tier) (*catalog.Tier, string) {
	switch {
	case work == nil:
		return nil, ""
	case req.MidTurn:
		return higher(work, cur), "mid-turn"
	case req.Peer:
		return higher(work, cur), "peer message"
	case req.FollowUp != "":
		return work, req.FollowUp
	case rd.relation == nil:
		return work, "work in progress"
	}
	top, p := rd.relationTop()
	if rd.separate() >= e.Catalog.Meta.RelationSeparateThreshold() {
		return nil, ""
	}
	return work, fmt.Sprintf("follow-up of the work in progress (%s %.2f)", top, p)
}

// higher returns the higher-ranked of two tiers (either may be nil).
func higher(a, b *catalog.Tier) *catalog.Tier {
	if a == nil || (b != nil && b.Rank > a.Rank) {
		return b
	}
	return a
}

// above is the tier ranked just above t (t itself at the top).
func (e *Env) above(scope string, t *catalog.Tier) *catalog.Tier {
	for _, x := range e.Catalog.ScoredTiers(scope) {
		if t == nil || x.Rank > t.Rank {
			return x
		}
	}
	return t
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
