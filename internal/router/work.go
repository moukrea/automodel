package router

import (
	"cmp"
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
//
// A new task below the work in progress is often a detour ("quick, fix the
// typo in the README" in the middle of a migration): the work it replaces
// is kept, paused, and a prompt that goes back to it ("back to the
// migration", or a bare "continue" once the detour is done) brings back
// its tier, mode and model. Another new task drops it, unless it is below
// it too (the detour goes on: "and the broken link in the install section
// too"); so do two hours. A go-ahead that goes back by default (the
// assistant asked something, and Jev doesn't say it offered more of the
// detour) keeps the detour, unless done, paused in turn (Kept): going back
// to it pauses the work again, and the next wrap-up closes the detour, not
// the work (it may be the detour's, if the assistant did offer more of it).
//
// An aside (a question unrelated to the work) gets its own level for that
// turn and changes nothing. A wrap-up marks the work done: from then on a
// question or a fact gets its own level too, and only more work on it (a
// go-ahead, an addition) reopens it and holds its level.

// WorkUpdate is what a decision makes of the session's work in progress.
type WorkUpdate struct {
	Kind  string // WorkNew, WorkSet, WorkRaised or WorkResumed
	Tier  string
	Mode  string
	Model string // the model it runs on besides the tiers' ("": the tier's)
	// Pause: new work below the work in progress, which waits, paused; on
	// WorkResumed, the detour waits in turn (a go-ahead went back to the
	// paused work by default, not because Jev read it going back).
	// KeepPaused: new work below the work already paused, which waits on
	// (a detour of a detour); of the two, the one that needs more waits,
	// the one paused first on a tie.
	Pause, KeepPaused bool
	// Done: a wrap-up closed the work (any other update opens it).
	Done bool
}

const (
	WorkNew        = "new"         // the prompt starts it: tier, mode and goal from this turn
	WorkSet        = "set"         // the user set its effort, mode or model in words
	WorkRaised     = "raised"      // a follow-up needed more
	WorkResumed    = "resumed"     // the prompt went back to the paused work
	WorkDone       = "done"        // a wrap-up closed it
	WorkReopened   = "reopened"    // more work on it after a wrap-up
	WorkDetourDone = "detour-done" // a wrap-up closed the detour kept paused, not the work
)

// Apply records the update on the session (prompt: the one decided on).
func (u *WorkUpdate) Apply(s *state.Session, prompt string, now time.Time) {
	if u == nil {
		return
	}
	if u.Kind == WorkDetourDone {
		s.Paused = nil
		return
	}
	w := state.Work{Tier: u.Tier, Mode: u.Mode, Model: u.Model, Since: now, Done: u.Done}
	prev := s.WorkInProgress()
	switch {
	case u.Kind == WorkResumed && s.Paused != nil:
		// Back by default, the detour waits (Kept); back to a detour kept
		// so, the work it went back to waits again.
		kept := s.Paused.Kept
		w.Goal, s.Paused = s.Paused.Goal, nil
		if (u.Pause || kept) && prev != nil && !prev.Done {
			p := *prev
			p.Since, p.Kept = now, u.Pause
			s.Paused = &p
		}
	case u.Kind != WorkNew && prev != nil:
		w.Goal, w.Since = prev.Goal, prev.Since
	default:
		w.Goal = head(state.NormalizePrompt(prompt), state.WorkGoalChars)
		if !u.KeepPaused {
			s.Paused = nil
		}
		if u.Pause && prev != nil {
			p := *prev
			p.Since = now
			s.Paused = &p
		}
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

// holdAt says whether the prompt follows the work up (work: the work in
// progress, or the paused work it goes back to), and then the tier the
// decision can't go below and why. Prompts that arrive while Claude works,
// and messages from other sessions, never lower the effort the turn runs
// at either (nor the work's, while it is open).
func (e *Env) holdAt(req Request, rd Reading, work, cur *catalog.Tier) (*catalog.Tier, string) {
	done := req.Work != nil && req.Work.Done && !e.resumes(req, rd)
	running := higher(work, cur)
	if done {
		running = cur
	}
	switch {
	case work == nil:
		return nil, ""
	case req.MidTurn && running != nil:
		return running, "mid-turn"
	case req.Peer && running != nil:
		return running, "peer message"
	case req.FollowUp != "" && !done:
		return work, req.FollowUp
	case req.FollowUp != "":
		return nil, "" // a compaction after the work was wrapped up
	case rd.relation == nil:
		return work, "work in progress"
	}
	top, p := rd.relationTop()
	switch {
	case e.resumes(req, rd):
		return work, fmt.Sprintf("back to the paused work (%s %.2f)", top, p)
	case req.ProposalGoAhead:
		// A go-ahead takes on what the assistant proposed: not below the
		// work, whatever its words read as ("yes" is no new task), unless
		// it is a wrap-up step or an aside, which Jev is sure of on its own
		// or which follows a done work (live: "looks good" to one more fix
		// of an open detour read wrap_up 0.45-0.49 and aside 0.28-0.33,
		// 0.77 separate in all, and dropped below the detour, closing it).
		if (top == catalog.RelationWrapUp || top == catalog.RelationAside) && (p >= e.Catalog.Meta.RelationSeparateThreshold() || done) {
			return nil, ""
		}
		return work, fmt.Sprintf("%s (%s %.2f)", FollowUpProposal, top, p)
	case rd.separate() >= e.Catalog.Meta.RelationSeparateThreshold():
		return nil, ""
	case done && top != catalog.RelationContinue && top != catalog.RelationExtend:
		return nil, "" // a question or a fact after the work was wrapped up
	}
	// The relation that holds: the likeliest that follows the work up (a
	// separate one may be likelier on its own, below the threshold).
	rel, q := rd.followTop()
	return work, fmt.Sprintf("follow-up of the work in progress (%s %.2f)", rel, q)
}

// resumes reports a prompt that goes back to the paused work: resume is
// Jev's likeliest relation, at relation_separate_threshold at least (it
// is only offered when there is paused work; it moves the session to
// another tier, often another model). A go-ahead to what the assistant
// asked after a detour is asked too: the question may offer to go back,
// to wrap the detour up or to do more of it. When the paused work needs
// more (BackFirst) such a go-ahead goes back to it, as a bare go-ahead
// does, unless Jev says the assistant offered one more thing for the
// detour (the offer question, at meta.detour_offer_threshold): "go" after
// "Anything else?" read continue up to 0.62 on the relation question, and
// a go-ahead that stays below the paused work is the lowering the owner
// rejects (backByDefault). A prompt typed mid-turn or sent by another
// session doesn't.
func (e *Env) resumes(req Request, rd Reading) bool {
	switch {
	case req.Paused == nil || req.MidTurn || req.Peer || req.FollowUp != "":
		return false
	}
	return e.sureResume(rd) || e.backByDefault(req, rd)
}

// sureResume: Jev reads the prompt going back to the paused work.
func (e *Env) sureResume(rd Reading) bool {
	top, p := rd.relationTop()
	return top == catalog.RelationResume && p >= e.Catalog.Meta.RelationSeparateThreshold()
}

// backByDefault: a go-ahead after a detour goes back to the paused work
// that needs more because the offer question doesn't say the assistant
// offered more of the detour, not because Jev read it going back. The
// detour, unless done, waits then (WorkUpdate.Pause): if the assistant
// did offer more of it and goes on with it, the next wrap-up or "back to
// the typo fix" still finds it.
func (e *Env) backByDefault(req Request, rd Reading) bool {
	return req.BackFirst && !req.MidTurn && !req.Peer && req.Paused != nil && req.FollowUp == "" &&
		!e.sureResume(rd) && rd.offer < e.Catalog.Meta.DetourOfferThreshold()
}

// Why a prompt follows the work up without the relation question: a
// go-ahead to what the assistant proposed ("Want me to fix it?"), routed
// but not below the work (with no paused work and the work open: after a
// detour the proposal may be to go back to it, and once the work is done
// it may be a wrap-up step; the relation question says so, see
// Request.ProposalGoAhead); the decision right after a compaction.
const (
	FollowUpProposal   = "go-ahead to a proposal"
	FollowUpCompaction = "compaction"
)

// HigherWork is the work that needs more of a and b (either may be nil):
// the higher tier, then the one with a mode; a on a tie.
func (e *Env) HigherWork(a, b *state.Work) *state.Work {
	if a == nil || b == nil {
		return cmp.Or(a, b)
	}
	ta, tb := e.Catalog.Tier(catalog.ScopeMain, a.Tier), e.Catalog.Tier(catalog.ScopeMain, b.Tier)
	switch {
	case tb == nil:
		return a
	case ta == nil || tb.Rank > ta.Rank:
		return b
	case tb.Rank == ta.Rank && a.Mode == "" && b.Mode != "":
		return b
	}
	return a
}

// GoAheadWork is the work a bare go-ahead carries on: the work in
// progress, or the paused work when that needs more (the detour is over:
// "ok, continue" goes back to the migration, not on at the typo's level;
// resumed then). Typed mid-turn, the detour is still running: it goes on
// with the turn, not to the paused work, and doesn't go below the
// decision the turn runs at (cur) nor drop its mode.
func (e *Env) GoAheadWork(cur *state.Decision, work, paused *state.Work, midTurn bool) (w *state.Work, resumed bool) {
	w = work
	if paused != nil && !midTurn && e.HigherWork(work, paused) == paused {
		w, resumed = paused, true
	}
	if !midTurn || w == nil || cur == nil {
		return w, resumed
	}
	t := e.Catalog.Tier(catalog.ScopeMain, cur.Tier)
	if t == nil {
		return w, resumed
	}
	k := *w
	if wt := e.Catalog.Tier(catalog.ScopeMain, k.Tier); wt == nil || t.Rank > wt.Rank {
		k.Tier = t.ID
	}
	if k.Mode == "" {
		k.Mode = cur.Mode
	}
	return &k, resumed
}

// Acknowledges reports a bare go-ahead that mostly acknowledges finished
// work, so the hooks route it (Jev's relation says whether it reopens the
// work): a wrap-up closed the work in progress, and no paused work needs
// more. After a detour that was wrapped up ("commit that"), "vas-y" goes
// back to the paused work, as it does before the wrap-up.
func (e *Env) Acknowledges(work, paused *state.Work) bool {
	back, _ := e.GoAheadWork(nil, work, paused, false)
	return work != nil && work.Done && back == work
}

// needsMore reports work a that needs strictly more than b: a higher
// tier, or the same with a mode b hasn't.
func (e *Env) needsMore(a, b *state.Work) bool {
	if a == nil || b == nil {
		return a != nil
	}
	ta, tb := e.Catalog.Tier(catalog.ScopeMain, a.Tier), e.Catalog.Tier(catalog.ScopeMain, b.Tier)
	switch {
	case ta == nil || tb == nil:
		return ta != nil
	case ta.Rank != tb.Rank:
		return ta.Rank > tb.Rank
	}
	return a.Mode != "" && b.Mode == ""
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
