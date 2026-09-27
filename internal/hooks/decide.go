package hooks

import (
	"context"
	"encoding/json"
	"github.com/moukrea/automodel/internal/policy"
	"regexp"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
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

const recentPrompts = 5

// Decide is the UserPromptSubmit hook. It decides the main-session tier at
// the moments the prompt cache is already lost (first prompt, compaction,
// cold cache) and, with features.warm_decisions, on warm turns too, where a
// switch has to beat its cost and clear the confidence bar.
func Decide(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	now := env.Now()
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	synthetic := transcript.IsSynthetic(in.Prompt)

	var tr *transcript.Info
	if sess.Model == "" {
		tr = readTranscript(in.TranscriptPath) // the model identity outranks settings
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

	// A user-chosen effort outranks routing: an [effort:X] tag pins it, an
	// [effort:auto] tag releases the pin (and this prompt is routed).
	tag := effortTag(in.Prompt)
	pin, pinSource := sess.Pin, sess.PinSource
	switch {
	case tag == "auto":
		pin, pinSource = "", ""
		if trigger == "" && sess.Main != nil && sess.Pin != "" && env.Cfg.Features.WarmDecisions {
			trigger = "warm"
		}
	case tag != "":
		pin, pinSource = tag, "prompt"
	}

	var dec *state.Decision
	var signals *state.RepoSignals
	if pin != "" && !synthetic {
		model := env.Catalog.DefaultTier(catalog.ScopeMain).Model
		if sess.Main != nil {
			model = sess.Main.Model
		}
		t := env.Catalog.TierFor(catalog.ScopeMain, model, pin)
		switch {
		case t == nil:
			pin, pinSource = "", "" // no such effort on this model: ignore the pin
		case tag != "" || trigger == "initial" || trigger == "compact" || trigger == "cold" ||
			sess.Main == nil || sess.Main.Tier != t.ID:
			dec = env.Pinned(in.SessionID, t, pinSource)
			trigger = "pinned-" + trigger
		}
	}
	if trigger == "warm" && pin == "" && env.Cfg.Features.FastPath && goAhead(in.Prompt) {
		// A bare go-ahead continues the work in progress: nothing to ask.
		env.LogKept(in.SessionID, sess.Main, "go-ahead: continues the work in progress")
		trigger = ""
	}
	if trigger != "" && pin == "" { // no routing while pinned
		if tr == nil && trigger != "initial" {
			tr = readTranscript(in.TranscriptPath)
		}
		signals = sess.Repo
		if signals == nil {
			signals = repo.Signals(ctx, in.Cwd)
		}
		req := mainRequest(env, in, sess, tr, signals, trigger)
		if trigger == "warm" {
			req.Warm, req.Current = true, sess.Main
			req.SwitchCost, req.Scale = switchCost(env, sess), workScale(env, sess)
		}
		var out router.Outcome
		dec, out = env.Decide(ctx, req)
		if !out.Changed {
			dec = nil
		}
	}

	var notice string
	_, err = env.State.Update(in.SessionID, func(s *state.Session) bool {
		s.LastPromptAt = now
		if !synthetic {
			s.Prompts++
		}
		if s.Repo == nil && signals != nil {
			s.Repo = signals
		}
		if s.Model == "" {
			s.Model, s.ModelSource = mi.Model, mi.Source
		}
		s.Pin, s.PinSource = pin, pinSource
		if dec != nil {
			prev := s.Main
			epoch := 1
			if prev != nil {
				epoch = prev.Epoch + 1
			}
			dec.Epoch = epoch
			s.Main = dec
			s.CompactPending, s.CompactTrigger, s.ColdHint = false, "", false
			switch {
			case trigger == "initial" || trigger == "compact" || trigger == "pinned-initial" || trigger == "pinned-compact":
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
		if s.Main == nil {
			return true
		}
		switch want := s.Main.Workflows; {
		case want && (!s.UltracodeOn || s.UltracodeEpoch != s.Main.Epoch):
			notice, s.UltracodeOn, s.UltracodeEpoch = UltracodeOn, true, s.Main.Epoch
		case !want && s.UltracodeOn:
			notice, s.UltracodeOn = UltracodeOff, false
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	if !synthetic && in.Prompt != "" {
		_ = env.State.IndexPrompt(in.Prompt, in.SessionID)
	}
	if notice == "" {
		return nil, nil
	}
	return &Output{HookSpecificOutput: &Specific{HookEventName: "UserPromptSubmit", AdditionalContext: notice}}, nil
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
		return float64(s.ContextTokens) * (write - cur.Price.CacheRead) / 1e6
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
	if asksMoreThinking(in.Prompt) {
		signals["asks_more_thinking"] = true
	}
	task := tokens.Truncate(in.Prompt, left/3)
	st["task"] = task
	left -= tokens.Estimate(task)
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
		if len(prev) > recentPrompts {
			prev = prev[len(prev)-recentPrompts:]
		}
		if len(prev) > 0 {
			var rp []string
			for _, p := range prev {
				rp = append(rp, tokens.Truncate(p, left/(4*recentPrompts)))
			}
			st["recent_prompts"] = rp
			left -= tokens.Estimate(mustJSON(rp))
		}
		if trigger != "compact" && tr.LastAssistant != "" {
			la := tokens.Truncate(tr.LastAssistant, min(1500, left/3))
			st["last_assistant"] = la
			left -= tokens.Estimate(la)
		}
	}
	if sess.Main != nil && trigger != "initial" {
		cur := map[string]any{"tier": sess.Main.Tier, "effort": sess.Main.Effort}
		if sess.Main.Mode != "" {
			cur["mode"] = sess.Main.Mode
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
		State: st, RepoDir: in.Cwd, Context: ctxTokens}
	if len(signals) > 0 {
		st["user_signals"] = signals
		req.Signals = signals
	}
	// Asking for more thinking is a floor: one tier above the current one.
	if signals["asks_more_thinking"] == true && sess.Main != nil {
		if cur := env.Catalog.Tier(catalog.ScopeMain, sess.Main.Tier); cur != nil {
			for _, t := range env.Catalog.TiersByRank(catalog.ScopeMain) {
				if t.Rank > cur.Rank {
					req.MinTier = t.ID
					break
				}
			}
		}
	}
	return req
}

// goAheads are prompts that only tell Claude to carry on.
var goAheads = map[string]bool{}

func init() {
	for _, p := range []string{"y", "yes", "yep", "yeah", "yup", "ok", "okay", "k", "sure", "go", "go ahead", "go on",
		"continue", "carry on", "keep going", "proceed", "do it", "lgtm", "sounds good", "looks good", "perfect", "great",
		"oui", "ouais", "ok go", "vas y", "vas-y", "go go", "continue stp", "continue please", "please continue", "yes please",
		"d'accord", "dac", "parfait", "fonce", "allez", "allez-y", "c'est bon", "c'est parti", "on y va", "ok vas-y", "oui vas-y"} {
		goAheads[p] = true
	}
}

var goAheadTrim = regexp.MustCompile(`[\s.!,;:]+$`)

// goAhead reports whether a prompt is only a go-ahead.
func goAhead(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = goAheadTrim.ReplaceAllString(p, "")
	p = strings.Join(strings.Fields(p), " ")
	return len(p) <= 24 && goAheads[p]
}

var moreThinkingRE = regexp.MustCompile(`(?i)\b(think (harder|more|deeply|carefully|it through)|ultrathink|take your time|be thorough|dig deeper|r[ée]fl[ée]chis (plus|bien|davantage|en profondeur)|prends (ton|le) temps|creuse (plus|bien|davantage))\b`)

// asksMoreThinking reports whether a prompt explicitly asks for more
// thinking.
func asksMoreThinking(prompt string) bool { return moreThinkingRE.MatchString(prompt) }

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
