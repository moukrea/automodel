package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/moukrea/automodel/internal/policy"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/install"
	"github.com/moukrea/automodel/internal/repo"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
	"github.com/moukrea/automodel/internal/transcript"
)

// UltracodeOn is injected as additionalContext when the ultracode mode is
// selected. Claude Code treats a direct request as the workflow opt-in; this
// states the standing opt-in the way the native setting's reminder does.
const UltracodeOn = `Ultracode is on for this session. The user enabled automatic routing, and the router turned on the ultracode mode for this work: this is the user's standing opt-in to workflow orchestration, equivalent to launching with --effort ultracode. Author and run a workflow (Workflow tool) for every substantive task by default, following the ultracode guidance of the workflow-authoring reference: decompose and cover in parallel, adversarially verify findings, and prefer several scoped workflows in sequence for multi-phase work. Work solo only on conversational turns or trivial mechanical edits. This stays in force until a reminder says ultracode is off.`

const UltracodeOff = `Ultracode is now off for this session (the automatic router moved back to single-thread work). Revert to the opt-in rule in the Workflow tool description.`

// UltracodeOffTurn is injected instead of UltracodeOff when the prompt is
// answered alone (a wrap-up, a side question, an aside) and the work in
// progress keeps the mode for its next follow-up.
const UltracodeOffTurn = `Ultracode is off for this turn only (the automatic router answers this prompt single-thread; the work in progress keeps ultracode, and a reminder will say when it is back on). For this answer, revert to the opt-in rule in the Workflow tool description.`

// Decide is the UserPromptSubmit hook. It decides the main-session tier at
// the moments the prompt cache is already lost (first prompt, compaction,
// cold cache) and, with features.warm_decisions, on warm turns too, where a
// switch has to beat its cost and clear the confidence bar. A prompt that
// follows the work in progress up keeps at least its tier (router.Judge).
func Decide(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	now := env.Now()
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	synthetic := transcript.IsSynthetic(in.Prompt) || ownCommand(in.Prompt)
	// A message from another Claude session is routed, but its words are
	// not the user's: no tag, no request in prose, no go-ahead.
	peer := transcript.IsPeer(in.Prompt)
	lateTrigger, lateAt, late := lateMode()
	if late && sess.LastPromptAt.UnixNano() != lateAt {
		return nil, nil // a newer prompt came in: its own decision stands
	}

	// A late decision decides on the assistant message the prompt's own
	// hook read: Claude may have written its answer to the prompt since.
	readTr := func(path string) *transcript.Info {
		t := readTranscript(path)
		if late && t != nil && in.LastAssistant != nil {
			t.LastAssistant = *in.LastAssistant
		}
		return t
	}
	var tr *transcript.Info
	if sess.Model == "" {
		tr = readTr(in.TranscriptPath) // the model identity outranks settings
	}
	mi := detectModel(env, sess, in, tr)
	// Only sessions positively on the routed model are touched: a session
	// on a named model (or one we can't identify) is never routed.
	if !env.IsCustom(mi.Model) {
		return nil, nil
	}

	trigger := ""
	switch {
	case sess.Main == nil:
		trigger = "initial"
	case sess.CompactPending:
		trigger = "compact"
	case sess.ColdHint:
		trigger = "cold"
	case !sess.LastActivity().IsZero() && now.Sub(sess.LastActivity()) > env.Cfg.CacheTTL.Duration:
		trigger = "cold"
	case env.Cfg.Features.WarmDecisions && strings.TrimSpace(in.Prompt) != "":
		trigger = "warm"
	}
	// Subagent hand-backs and other injected prompts carry no task: never
	// decide on them.
	if synthetic {
		trigger = ""
	}
	if late {
		trigger = lateTrigger // what the prompt's own hook was deciding
	}

	// A user-chosen effort or model outranks routing: [effort:X] pins the
	// effort on the session's model, [model:X] pins a model (with the
	// effort of [effort:X], else the current one), [effort:auto] or
	// [model:auto] releases the pin (and this prompt is routed). Only
	// prompts the user typed count: subagent results and notifications can
	// quote a tag.
	typed := !synthetic && !peer
	etag, mtag := "", ""
	if typed {
		etag, mtag = effortTag(in.Prompt), modelTag(env.Catalog, in.Prompt)
	}
	curModel, curEffort, curMode := env.Catalog.DefaultTier(catalog.ScopeMain).Model, "", ""
	if sess.Main != nil {
		curModel, curEffort, curMode = sess.Main.Model, sess.Main.Effort, sess.Main.Mode
	}
	pin, pinModel, pinSource := sess.Pin, sess.PinModel, sess.PinSource
	// An effort on a session that routing put on an asked tier (Haiku) or on
	// a model without that effort among the tiers means the default model at
	// that effort: [effort:high] must not be ignored, nor pin the asked tier.
	// A model a work asked for in words keeps the tag (below).
	if etag != "" && etag != "auto" && sess.PinModel == "" && (sess.Work == nil || sess.Work.Model != curModel) {
		if t := env.Catalog.TierFor(catalog.ScopeMain, curModel, etag); t == nil || t.Asked() {
			curModel = env.Catalog.DefaultTier(catalog.ScopeMain).Model
		}
	}
	switch {
	case etag == "auto" || mtag == "auto":
		pin, pinModel, pinSource = "", "", ""
		if trigger == "" && sess.Main != nil && sess.Pin != "" && env.Cfg.Features.WarmDecisions {
			trigger = "warm"
		}
	case mtag != "":
		if eff := modelEffort(env, mtag, etag, curEffort); eff != "" {
			pin, pinModel, pinSource = eff, mtag, "prompt"
		}
	case etag != "":
		w := sess.Work
		switch {
		case pinModel != "":
			if m := env.Catalog.Model(pinModel); m != nil && m.SupportsEffort(etag) {
				pin, pinSource = etag, "prompt"
			}
		case w != nil && w.Model != "" && w.Model == curModel:
			// On the model a work asked for in words: the tag pins it there
			// (unless the catalog no longer has it).
			if m := env.Catalog.Model(w.Model); m != nil && m.SupportsEffort(etag) {
				pin, pinModel, pinSource = etag, w.Model, "prompt"
			}
		case env.Catalog.TierFor(catalog.ScopeMain, curModel, etag) != nil:
			pin, pinSource = etag, "prompt"
		} // else: an effort this model doesn't have: ignored
	}
	tagged := (etag != "" && etag != "auto") || (mtag != "" && mtag != "auto")

	var dec *state.Decision
	var signals *state.RepoSignals
	var work *router.WorkUpdate // what the decision makes of the work in progress
	var asked *router.Asked     // an effort or a model the prompt asked for in words
	var goalFor string          // a goal named from the recent prompts for a work that has none
	var handedBack *state.Work  // the work done while pinned, when the pin is handed back
	turnOnly := false           // answered alone, without the mode the work keeps
	spawnTrigger := ""          // Jev timed out: decide again in the background
	// A pin keeps the ultracode mode unless its effort is below the mode's.
	if pin != "" && !synthetic {
		redecide := tagged || trigger == "initial" || trigger == "compact" || trigger == "cold" || sess.Main == nil
		if pinModel != "" {
			if redecide || sess.Main.Model != pinModel || sess.Main.Effort != pin {
				dec = env.PinnedModel(in.SessionID, repoRoot(sess, in.Cwd), pinModel, pin, curMode, pinSource)
				trigger = "pinned-" + trigger
			}
		} else if t := env.Catalog.TierFor(catalog.ScopeMain, curModel, pin); t == nil {
			pin, pinSource = "", "" // no such effort on this model: ignore the pin
		} else if redecide || sess.Main.Tier != t.ID {
			dec = env.Pinned(in.SessionID, repoRoot(sess, in.Cwd), t, curMode, pinSource)
			trigger = "pinned-" + trigger
		}
	}
	// A bare go-ahead continues the work in progress, without asking Jev,
	// which rates the bare word as trivial: the tier and mode the work was
	// decided at come back (a side question since may have lowered them),
	// on a warm turn, after a compaction or after a pause; after a detour,
	// the paused work when it needs more. A go-ahead to a proposal ("Want
	// me to fix it?") starts that work, which may be bigger: it is routed,
	// not below the work in progress. After a detour (paused work), what
	// the assistant asked may offer to go back to the paused work, a
	// wrap-up step of the detour ("Want me to push it?") or more of it:
	// routed with the relation question, which offers resume; when the
	// paused work needs more, the go-ahead goes back there unless Jev says
	// the assistant offered one more thing for the detour ("Anything
	// else?" offers nothing of it). Once a wrap-up closed the work, "ok"
	// or "looks good" mostly acknowledges it: routed, Jev's relation says
	// whether it reopens the work; unless that work was a detour and the
	// paused work needs more, which a bare go-ahead goes back to. A
	// go-ahead to a proposal there is routed with the relation question
	// too, but never starts a work of its own ("yes" to "Want me to add
	// the same check to the importer?" is no goal, nor below the work);
	// right after a compaction, whose summary may end on such a proposal,
	// a go-ahead once the work is done is taken as one.
	followUp := ""
	proposalGoAhead, backFirst := false, false
	wip := sess.WorkInProgress()
	back, resumed := env.GoAheadWork(sess.Main, wip, sess.PausedWork(now), false)
	if pin == "" && sess.Main != nil && env.Cfg.Features.FastPath && typed && goAhead(in.Prompt) &&
		(trigger == "warm" || trigger == "compact" || trigger == "cold") &&
		!env.AboveCap(in.SessionID, catalog.ScopeMain, env.Catalog.Tier(catalog.ScopeMain, sess.Main.Tier)) &&
		(back == nil || !env.AboveCap(in.SessionID, catalog.ScopeMain, env.Catalog.Tier(catalog.ScopeMain, back.Tier))) {
		if tr == nil {
			tr = readTr(in.TranscriptPath)
		}
		proposes := trigger != "compact" && tr != nil && router.Proposes(tr.LastAssistant)
		switch {
		case env.Acknowledges(wip, sess.PausedWork(now)):
			proposalGoAhead = proposes || trigger == "compact" // routed as any prompt otherwise
		case proposes && sess.PausedWork(now) == nil:
			followUp = router.FollowUpProposal
		case proposes:
			proposalGoAhead, backFirst = true, resumed // not mid-turn: the router checks
		default:
			// Typed mid-turn, it lowers nothing.
			if dec, work = env.Carry(router.Request{SessionID: in.SessionID, Scope: catalog.ScopeMain, Trigger: trigger, RepoDir: in.Cwd,
				Context: sess.ContextTokens, Current: sess.Main, Work: wip, Paused: sess.PausedWork(now),
				MidTurn: tr.MidTurnFor(in.Prompt)}); dec == nil {
				trigger = "" // kept
			} else {
				trigger = "carried-" + trigger
			}
		}
	}
	if trigger != "" && pin == "" && dec == nil { // no routing while pinned or carried
		if tr == nil && trigger != "initial" {
			tr = readTr(in.TranscriptPath)
		}
		signals = sess.Repo
		if signals == nil {
			signals = repo.Signals(ctx, in.Cwd)
		}
		// Handing a pin back: the work done while pinned was never recorded
		// (routing was off). It is what the recent prompts asked, at the
		// level it ran at, so Jev relates this prompt to it (router.Judge
		// then judges that work again).
		released := (etag == "auto" || mtag == "auto") && sess.Pin != ""
		reqSess := sess
		if released && sess.WorkInProgress() == nil && sess.Main != nil && tr != nil {
			if g := recentGoal(tr.UserPrompts, in.Prompt); g != "" {
				t := env.Catalog.Tier(catalog.ScopeMain, sess.Main.Tier)
				if t == nil {
					t = env.Catalog.DefaultTier(catalog.ScopeMain)
				}
				handedBack = &state.Work{Tier: t.ID, Goal: g, Since: now}
				s := *sess
				s.Work = handedBack
				reqSess = &s
			}
		}
		req := mainRequest(env, in, reqSess, tr, signals, trigger)
		req.FollowUp, req.ProposalGoAhead, req.BackFirst = followUp, proposalGoAhead, backFirst
		req.Released = released
		if req.Work != nil && (sess.Work == nil || sess.Work.Goal == "") {
			goalFor = req.Work.Goal
		}
		// [model:auto] / [effort:auto] handing a pin back: say so in the
		// ledger (and to Jev) — the move off a pinned model is the user's call.
		if (etag == "auto" || mtag == "auto") && sess.Pin != "" {
			if req.Signals == nil {
				req.Signals = map[string]any{}
				req.State["user_signals"] = req.Signals
			}
			req.Signals["released_pin"] = true
			if sess.PinModel != "" {
				req.Signals["released_pin_model"] = sess.PinModel
			}
		}
		if trigger == "warm" {
			req.Warm, req.Current = true, sess.Main
			req.SwitchCost, req.Scale = switchCost(env, sess), workScale(env, sess)
		}
		if late {
			req.Timeout = LateTimeout
		}
		var out router.Outcome
		dec, out = env.Decide(ctx, req)
		if out.TimedOut && !late {
			spawnTrigger = trigger
		}
		work, asked, turnOnly = out.Work, out.Asked, out.TurnOnly
		if !out.Changed || (late && out.TimedOut) {
			dec = nil
		}
		if late && out.TimedOut {
			work = nil
		}
	}

	var notice string
	if late && dec == nil && work == nil {
		return nil, nil
	}
	_, err = env.State.Update(in.SessionID, func(s *state.Session) bool {
		// What a late decision could not say: said by this prompt's hook,
		// if this prompt goes on with the work it was asked for.
		var pending *router.Asked
		pendingSince := time.Time{}
		if !late && s.PendingAsked != nil {
			pending, pendingSince = &router.Asked{Effort: s.PendingAsked.Effort, Model: s.PendingAsked.Model}, s.PendingAsked.WorkSince
			s.PendingAsked = nil
		}
		if late {
			if s.LastPromptAt.UnixNano() != lateAt {
				dec = nil // a newer prompt came in while Jev answered
				return false
			}
			if dec != nil {
				log.Printf("late decision %s: %s", in.SessionID, dec.Tier)
			}
		} else {
			s.LastPromptAt = now
		}
		if !synthetic && !late {
			s.Prompts++
		}
		if tr != nil {
			s.ClientUltracode = tr.Ultracode
		}
		if s.Repo == nil && signals != nil {
			s.Repo = signals
		}
		if s.Model == "" {
			s.Model, s.ModelSource = mi.Model, mi.Source
		}
		if !synthetic {
			s.Pin, s.PinModel, s.PinSource = pin, pinModel, pinSource
		}
		if s.Work == nil && handedBack != nil {
			s.Work = handedBack
		}
		if s.Work == nil && (dec != nil || work != nil) {
			s.Work = s.WorkInProgress() // a session from before: the decision in force was its work
		}
		if s.Work != nil && s.Work.Goal == "" && goalFor != "" {
			s.Work.Goal = goalFor // named from the recent prompts (mainRequest)
		}
		if s.Paused != nil && s.PausedWork(now) == nil {
			s.Paused = nil // paused too long ago to be resumed
		}
		work.Apply(s, in.Prompt, now)
		if dec != nil {
			prev := s.Main
			epoch := 1
			if prev != nil {
				epoch = prev.Epoch + 1
			}
			dec.Epoch = epoch
			switch {
			case asked != nil:
				dec.Why, dec.WhyP = "asked", 0
			case dec.Why == "" && trigger == "initial":
				dec.Why = "new"
			}
			dec.From = ""
			if prev != nil && prev.Effort != dec.Effort && prev.Effort != "" {
				dec.From = prev.Effort
			}
			s.Main = dec
			s.CompactPending, s.CompactTrigger, s.ColdHint = false, "", false
			switch {
			case trigger == "initial" || strings.HasSuffix(trigger, "compact") || trigger == "pinned-initial":
				// A new conversation for the API: fresh top-level effort.
				s.ResetEffortEpoch()
				if strings.HasSuffix(trigger, "compact") {
					s.ContextTokens = 0 // stale until the next response
				}
			case prev == nil || prev.Model != dec.Model:
				s.ResetEffortEpoch() // a model switch rebuilds the cache anyway
			case prev.Effort != dec.Effort && perTurnOK(env, s, dec.Model):
				s.PendingEffort = &state.PendingEffort{Effort: dec.Effort, Prompt: head(state.NormalizePrompt(in.Prompt), PendingPromptChars), CreatedAt: now}
			case prev.Effort != dec.Effort:
				s.ResetEffortEpoch() // top-level effort change: the policy accepted the rebuild
			}
		}
		if late && s.Main != nil && dec != nil && asked != nil && !asked.Turn && s.Work != nil {
			s.PendingAsked = &state.Asked{Effort: asked.Effort, Model: asked.Model, WorkSince: s.Work.Since}
		}
		if s.Main == nil || late {
			return true // a late decision can't inject a notice: the next prompt does
		}
		switch want := s.Main.Workflows; {
		case want && (!s.UltracodeOn || s.UltracodeEpoch != s.Main.Epoch):
			notice, s.UltracodeOn, s.UltracodeEpoch = UltracodeOn, true, s.Main.Epoch
		case !want && s.UltracodeOn:
			// Off for this turn only when the router answers this prompt
			// alone and the work keeps the mode; a pin or the budget cap
			// keeps it off.
			notice, s.UltracodeOn = UltracodeOff, false
			if turnOnly {
				notice = UltracodeOffTurn
			}
		}
		if asked == nil && pending != nil && s.Work != nil && s.Work.Since.Equal(pendingSince) {
			asked = pending // not on another work (a new task, the paused work resumed)
		}
		if n := askedNotice(env.Catalog, asked, s.Main); n != "" {
			notice = strings.TrimSpace(notice + "\n\n" + n)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if spawnTrigger != "" {
		if tr != nil {
			la := tr.LastAssistant
			in.LastAssistant = &la
		}
		spawnLate(in, spawnTrigger, now)
	}
	if !synthetic && in.Prompt != "" && !late {
		_ = env.State.IndexPrompt(in.Prompt, in.SessionID)
	}
	if notice == "" {
		return nil, nil
	}
	return &Output{HookSpecificOutput: &Specific{HookEventName: "UserPromptSubmit", AdditionalContext: notice}}, nil
}

// askedNotice tells Claude that the effort or the model the user asked
// for in words runs (d: the decision in force): without it Claude answers
// that it can't change its own effort, or hands the work to a subagent on
// the model asked. "" when nothing was asked, or the decision doesn't run
// it (the budget cap, the repo's bounds).
func askedNotice(c *catalog.Catalog, a *router.Asked, d *state.Decision) string {
	if a == nil || d == nil {
		return ""
	}
	var on []string
	after := "(the automatic router already applied it)"
	if a.Model != "" {
		m := c.Model(a.Model)
		if m == nil || d.Model != a.Model {
			return ""
		}
		on = append(on, "on "+m.Label)
		after = "(the automatic router already switched the model: no subagent is needed for that)"
	}
	if a.Effort != "" {
		if d.Effort != a.Effort {
			return ""
		}
		on = append(on, "at "+a.Effort+" effort")
	}
	what := "this work now runs"
	if a.Turn {
		what = "this answer runs"
	}
	return fmt.Sprintf("automodel: %s %s, as the user asked %s.", what, strings.Join(on, " "), after)
}

// modelEffort is the effort a model pin runs at: the first of efforts the
// model supports, else its default tier's, else high ("" if none).
func modelEffort(env *router.Env, model string, efforts ...string) string {
	m := env.Catalog.Model(model)
	for _, e := range append(efforts, env.Catalog.DefaultTier(catalog.ScopeMain).Effort, "high") {
		if e != "" && m.SupportsEffort(e) {
			return e
		}
	}
	return ""
}

// perTurnOK reports whether an effort change on model keeps the prompt
// cache: the feature is on, the model supports per-turn effort and the API
// hasn't refused it in this conversation.
func perTurnOK(env *router.Env, s *state.Session, model string) bool {
	m := env.Catalog.Model(model)
	return env.Cfg.Features.PerTurnEffort && m != nil && m.PerTurnEffort &&
		env.Catalog.Meta.PerTurnEffortBeta != "" && !s.PerTurnRejected
}

// switchCost is the dollar cost of moving the session to a tier: nothing
// for the same model and effort, nothing for an effort change that keeps
// the cache (per-turn effort), otherwise the whole context written to the
// cache again instead of read from it.
func switchCost(env *router.Env, s *state.Session) func(*catalog.Tier) float64 {
	cat := env.Catalog
	cur := cat.Model(s.Main.Model)
	return func(t *catalog.Tier) float64 {
		if t.Model == s.Main.Model && (t.Effort == s.Main.Effort || perTurnOK(env, s, t.Model)) {
			return 0
		}
		m := cat.Model(t.Model)
		if m == nil || m.Price == nil || cur == nil || cur.Price == nil {
			return 0
		}
		write := m.Price.CacheWrite5m
		if env.Cfg.CacheTTL.Duration >= time.Hour && m.Price.CacheWrite1h > 0 {
			write = m.Price.CacheWrite1h
		}
		c := float64(s.ContextTokens) * (write - cur.Price.CacheRead) / 1e6
		// A tier with a small window is a stop on the way: the session
		// comes back to the current model, and pays that rebuild too.
		if t.MaxContext > 0 && t.Model != s.Main.Model {
			back := cur.Price.CacheWrite5m
			if env.Cfg.CacheTTL.Duration >= time.Hour && cur.Price.CacheWrite1h > 0 {
				back = cur.Price.CacheWrite1h
			}
			c += float64(s.ContextTokens) * (back - m.Price.CacheRead) / 1e6
		}
		return c
	}
}

// workScale converts catalog cost units into dollars for the work a switch
// would serve: the session's observed cost per prompt, relative to the
// current tier's catalog cost, times the switch horizon.
func workScale(env *router.Env, s *state.Session) float64 {
	h := env.Cfg.Features.SwitchHorizonPrompts
	k := 1.0
	if t := env.Catalog.Tier(catalog.ScopeMain, s.Main.Tier); t != nil && s.Prompts >= 2 && s.SpendUSD > 0 {
		if c := policy.Costs(env.Catalog, catalog.ScopeMain)[t.ID]; c > 0 {
			k = s.SpendUSD / float64(s.Prompts) / c
		}
	}
	return k * h
}

func mainRequest(env *router.Env, in *Input, sess *state.Session, tr *transcript.Info, repoSignals *state.RepoSignals, trigger string) router.Request {
	budget := env.Budget(catalog.ScopeMain)
	phase := map[string]string{"initial": "initial", "compact": "post_compact", "cold": "resumed", "warm": "warm"}[trigger]
	reserve := 1500
	if budget-reserve < 1000 {
		reserve = 0
	}
	left := budget - reserve
	st := map[string]any{"phase": phase}
	signals := map[string]any{}
	task := tokens.Truncate(in.Prompt, left/3)
	if task == "" && trigger == "compact" {
		task = "(no new prompt yet: the work the compaction summary says comes next)"
	}
	st["task"] = task
	left -= tokens.Estimate(task)
	// The work in progress, for Jev to relate the prompt to: the prompt that
	// started it (also after a compaction, when recent prompts are gone)
	// and the level it was decided at; and the work a detour paused, which
	// the prompt may go back to.
	var work, paused, shown *state.Work
	if trigger != "initial" {
		work, paused = sess.WorkInProgress(), sess.PausedWork(env.Now())
	}
	if work != nil && work.Goal == "" && tr != nil {
		// A work from before goals were kept: its goal is what the recent
		// prompts asked, so Jev can relate a prompt to it, and find it
		// again once a detour paused it (live: a disk cleanup asked during
		// a hard rendering work paused it as {level: high}, with nothing
		// for a later "et WP5 ?" to go back to).
		if g := recentGoal(tr.UserPrompts, in.Prompt); g != "" {
			w := *work
			w.Goal = g
			work = &w
		}
	}
	if router.Resumable(paused) {
		shown = paused // not a kept detour, below the work in progress
	}
	for _, w := range []struct {
		key  string
		work *state.Work
	}{{"work_in_progress", work}, {"paused_work", shown}} {
		if w.work == nil {
			continue
		}
		m := map[string]any{}
		if lv := router.WorkLevel(env.Catalog, w.work.Tier); lv != "" {
			m["level"] = lv
		}
		if w.work.Done {
			m["done"] = true // a wrap-up closed it
		}
		if w.work.Goal != "" {
			g := tokens.Truncate(w.work.Goal, left/8)
			m["goal"] = g
			left -= tokens.Estimate(g)
		}
		st[w.key] = m
	}
	ctxTokens := sess.ContextTokens
	if trigger == "compact" {
		ctxTokens = 0 // the pre-compaction size is peak_context_tokens
	}
	if trigger == "compact" && tr != nil && tr.CompactSummary != "" {
		sum := tokens.Truncate(tr.CompactSummary, left*2/3)
		st["compaction_summary"] = sum
		left -= tokens.Estimate(sum)
		ctxTokens = tokens.Estimate(tr.CompactSummary)
	}
	if trigger != "initial" && tr != nil {
		prev := tr.UserPrompts
		if n := len(prev); n > 0 && strings.TrimSpace(prev[n-1]) == strings.TrimSpace(in.Prompt) {
			prev = prev[:n-1]
		}
		// The last turn was stopped by the user: likely the wrong effort.
		if tr.Interrupted && tr.InterruptedAt >= len(prev) {
			signals["previous_turn_interrupted"] = true
		}
		if len(prev) > env.Catalog.State.RecentPromptsN() {
			prev = prev[len(prev)-env.Catalog.State.RecentPromptsN():]
		}
		if len(prev) > 0 {
			var rp []string
			for _, p := range prev {
				rp = append(rp, tokens.Truncate(p, left/(4*env.Catalog.State.RecentPromptsN())))
			}
			st["recent_prompts"] = rp
			left -= tokens.Estimate(mustJSON(rp))
		}
		// After a compaction the prompts before it are gone: its summary
		// stays until enough new ones say what the work is about.
		if _, ok := st["compaction_summary"]; !ok && tr.CompactSummary != "" && len(prev) < env.Catalog.State.RecentPromptsN() {
			sum := tokens.Truncate(tr.CompactSummary, left/3)
			st["compaction_summary"] = sum
			left -= tokens.Estimate(sum)
		}
		if trigger != "compact" && tr.LastAssistant != "" {
			la := tokens.Truncate(tr.LastAssistant, min(env.Catalog.State.LastAssistantN(), left/3))
			st["last_assistant"] = la
			left -= tokens.Estimate(la)
		}
	}
	if sess.Main != nil && trigger != "initial" {
		cur := map[string]any{"tier": sess.Main.Tier, "effort": sess.Main.Effort}
		if sess.Main.Mode != "" {
			cur["mode"] = sess.Main.Mode
		}
		if m := env.Catalog.Model(sess.Main.Model); sess.Main.Tier == state.PinnedTier && m != nil {
			delete(cur, "tier") // a model outside the tiers, asked for this work
			cur["model"] = m.Label
		}
		st["current"] = cur
	}
	session := map[string]any{}
	if ctxTokens > 0 {
		session["context_tokens"] = ctxTokens
	}
	// A session that was compacted is long-running: its size before the
	// compaction says more about the work than the summary does.
	if sess.Compactions > 0 {
		session["compactions"] = sess.Compactions
	}
	if sess.PeakContextTokens > ctxTokens {
		session["peak_context_tokens"] = sess.PeakContextTokens
	}
	if len(session) > 0 {
		st["session"] = session
	}
	if r := router.FitRepo(repoSignals, left-50); r != nil {
		st["repo"] = r
	}
	req := router.Request{SessionID: in.SessionID, Scope: catalog.ScopeMain, Trigger: trigger,
		State: st, RepoDir: in.Cwd, Context: ctxTokens, RepoRoot: repoRoot(sess, in.Cwd), Work: work, Paused: paused}
	if repoSignals != nil && repoSignals.Root != "" {
		req.RepoRoot = repoSignals.Root
	}
	if len(signals) > 0 {
		st["user_signals"] = signals
		req.Signals = signals
	}
	// A prompt typed while Claude works, or a message from another
	// session, never lowers the effort the work runs at.
	req.MidTurn = trigger != "initial" && tr.MidTurnFor(in.Prompt)
	// A scheduled task's prompt (a cron the session set up, a wakeup) is no
	// prompt the user typed: like another session's message it never
	// lowers the work nor replaces it (live: a status check every 30 min
	// read as new low work each time and kept the real work paused).
	// One that isn't more of the work (a status check) takes its own level.
	req.Scheduled = !transcript.IsPeer(in.Prompt) && tr != nil && tr.IsScheduled(in.Prompt)
	req.Peer = transcript.IsPeer(in.Prompt) || req.Scheduled
	req.GoAhead = !req.Peer && goAhead(in.Prompt)
	req.EndsGoAhead = !req.Peer && router.EndsWithGoAhead(in.Prompt)
	if !req.Peer {
		// What the user may ask for in words: Jev tells a request from a mention;
		// ultrathink is a keyword, a floor at xhigh.
		model := env.Catalog.DefaultTier(catalog.ScopeMain).Model
		if sess.Main != nil {
			model = sess.Main.Model
		}
		req.Explicit = router.ExplicitRequests(env.Catalog, model)
		if t := router.EffortTier(env.Catalog, model, "xhigh"); t != nil && router.Ultrathink(in.Prompt) {
			req.MinTier = t.ID
		}
	}
	return req
}

// repoRoot is the session's repository root: the one recorded, else the
// nearest parent of cwd with a .git, else cwd.
func repoRoot(sess *state.Session, cwd string) string {
	if sess.Repo != nil && sess.Repo.Root != "" {
		return sess.Repo.Root
	}
	for d := cwd; d != ""; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			break
		}
	}
	return cwd
}

// goAhead reports whether a prompt is only a go-ahead.
func goAhead(prompt string) bool { return router.GoAhead(prompt) }

// ownCommand reports automodel's own slash commands (/why, /flag): they
// only print what automodel knows, so they are neither routed nor logged,
// and /flag keeps pointing at the user's real last prompt.
func ownCommand(prompt string) bool {
	p := strings.TrimSpace(prompt)
	for _, c := range []string{"/why", "/flag"} {
		if p == c || strings.HasPrefix(p, c+" ") {
			return true
		}
	}
	return strings.Contains(prompt, install.CommandMarker)
}

var modelTagRE = regexp.MustCompile(`(?i)\[model:\s*([a-z0-9._-]+)\s*\]`)

// modelTag returns the catalog model a [model:X] tag asks for (by alias,
// catalog key or API ID), "auto" to release a pin, or "". Only models that
// can run a main session count: an API ID and at least the main window.
func modelTag(c *catalog.Catalog, prompt string) string {
	m := modelTagRE.FindAllStringSubmatch(prompt, -1)
	if len(m) == 0 {
		return ""
	}
	want := strings.ToLower(m[len(m)-1][1])
	if want == "auto" {
		return "auto"
	}
	return router.MainModel(c, want)
}

var effortTagRE = regexp.MustCompile(`(?i)\[effort:\s*(low|medium|high|xhigh|max|auto)\s*\]`)

// effortTag returns the effort an [effort:X] tag in the prompt asks for
// ("auto" releases a pin), or "".
func effortTag(prompt string) string {
	m := effortTagRE.FindAllStringSubmatch(prompt, -1)
	if len(m) == 0 {
		return ""
	}
	return strings.ToLower(m[len(m)-1][1])
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// recentGoal names a work that has no goal from the prompts before this
// one: the last three that are not bare go-aheads, oldest first.
func recentGoal(prompts []string, prompt string) string {
	var picked []string
	for i := len(prompts) - 1; i >= 0 && len(picked) < 3; i-- {
		p := strings.TrimSpace(prompts[i])
		if p == "" || p == strings.TrimSpace(prompt) || router.GoAhead(p) {
			continue
		}
		picked = append([]string{head(p, 160)}, picked...)
	}
	if len(picked) == 0 {
		return ""
	}
	return "(from the recent prompts) " + strings.Join(picked, " / ")
}
