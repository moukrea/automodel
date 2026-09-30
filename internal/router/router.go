// Package router is the decision engine shared by the hooks: it builds the
// Jev state, calls Jev (and the shadow model), applies the policy and records
// the decision in the ledger.
package router

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/jev"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/netx"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
)

type Env struct {
	Cfg     *config.Config
	Catalog *catalog.Catalog
	State   state.Store
	Ledger  ledger.Ledger
	States  ledger.States // routing states sent to Jev (record_states)
	Jev     *jev.Client
	Now     func() time.Time
}

// New loads the catalog (falling back to the last good copy) and wires the env.
func New(cfg *config.Config) (*Env, error) {
	if cfg.KeyFileTooOpen() {
		log.Printf("warning: %s holds openrouter_api_key but is readable by others (chmod 600)", cfg.Path())
	}
	cat, err := NewStore(cfg).Get()
	if err != nil {
		return nil, err
	}
	return &Env{
		Cfg:     cfg,
		Catalog: cat,
		State:   state.Store{Dir: cfg.StateDir},
		Ledger:  ledger.Ledger{Path: cfg.Ledger},
		States:  statesFor(cfg),
		Jev:     &jev.Client{URL: cfg.JevURL, APIKey: cfg.APIKey(), HTTP: JevHTTP(cfg)},
		Now:     time.Now,
	}, nil
}

// JevDialer dials the Jev endpoint, falling back to the addresses it last
// resolved to when the system resolver is slow (netx).
func JevDialer(cfg *config.Config) *netx.Dialer {
	return &netx.Dialer{Cache: filepath.Join(cfg.StateDir, "dns.json"), Dialer: net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}}
}

// JevHTTP is the HTTP client of Jev calls.
func JevHTTP(cfg *config.Config) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = JevDialer(cfg).DialContext
	return &http.Client{Transport: tr}
}

// JevHost is the host of the Jev endpoint ("" if the URL doesn't parse).
func JevHost(cfg *config.Config) string {
	u, err := url.Parse(cfg.JevURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// IsCustom reports whether a model string designates the routed model.
func (e *Env) IsCustom(model string) bool {
	return strings.TrimSuffix(model, "[1m]") == e.Cfg.CustomModelID
}

// Request is one routing question.
type Request struct {
	SessionID string
	Scope     string
	Trigger   string // initial|compact|cold|warm (main) or agent|workflow (subagent)
	AgentType string
	State     map[string]any
	RepoDir   string
	RepoRoot  string // recorded in the ledger (report suggestions)
	Context   int

	// Warm turns (the cache is intact): Current is the decision in force,
	// SwitchCost the dollar cost of moving to a tier (cache rebuild), Scale
	// the dollars per catalog cost unit of the work ahead.
	// Signals the user gave (interrupted turn); MinTier is a floor the
	// prompt sets without asking Jev (the ultrathink keyword).
	Signals map[string]any
	MinTier string

	// Work is the session's work in progress (main scope; nil on a first
	// prompt): a prompt that follows it up keeps at least its tier, its
	// mode and its model. Paused is the work a detour set aside, which the
	// prompt may go back to. MidTurn: the prompt came in while Claude was
	// still working; Peer: another Claude session sent it. Neither lowers
	// the effort. FollowUp is why the prompt follows the work up without
	// asking Jev (a go-ahead to a proposal, a compaction).
	Work, Paused  *state.Work
	MidTurn, Peer bool
	FollowUp      string
	// Explicit are the requests the prompt's words may make, for Jev to
	// confirm.
	Explicit []Candidate

	Warm bool
	// Label names a workflow stage (its label or phase option), for the ledger.
	Label string
	// Timeout overrides the Jev timeout (late decisions get longer).
	Timeout    time.Duration
	Current    *state.Decision
	SwitchCost func(*catalog.Tier) float64
	Scale      float64
}

// Outcome says what a warm decision did.
type Outcome struct {
	Changed bool
	Reason  string // why a warm decision kept the current tier
	// TimedOut: Jev didn't answer in time; the decision above is the
	// fallback (a late decision can still replace it).
	TimedOut bool
	// Work is what becomes of the session's work in progress (nil: nothing).
	Work *WorkUpdate
}

// Decide asks Jev (and the shadow model, if configured) and applies the
// policy. It never fails: on errors a warm turn keeps its tier and other
// triggers get the scope's default tier (trigger "fallback"). The decision
// is appended to the ledger.
func (e *Env) Decide(ctx context.Context, req Request) (*state.Decision, Outcome) {
	c := e.Catalog
	f := e.Cfg.Features
	rp := policy.LoadRepoPolicy(req.RepoDir, e.Cfg.RepoPolicyFile)
	ask := jev.Ask{Relation: req.Work != nil && req.FollowUp == ""}
	ask.Resume = ask.Relation && req.Paused != nil
	// A repository can only make privacy stricter, never looser. Without
	// the prompt's text Jev can't confirm what it asks for.
	metadata := rp.Privacy == PrivacyMetadata || e.Cfg.Privacy == PrivacyMetadata
	task, _ := req.State["task"].(string)
	if metadata {
		req.State = MetadataOnly(req.State)
	} else {
		for _, x := range req.Explicit {
			ask.Explicit = append(ask.Explicit, x.Explicit)
		}
	}
	qs, ids := jev.Questions(c, req.Scope, ask)
	stateTokens := tokens.Estimate(mustJSON(req.State))
	start := e.Now()
	var cur *catalog.Tier
	if req.Warm && req.Current != nil {
		cur = c.Tier(req.Scope, req.Current.Tier)
	}
	rec := ledger.Decision{
		TS: start, Kind: "decision", ID: ledger.NewID(), SessionID: req.SessionID, Scope: req.Scope, Trigger: req.Trigger,
		AgentType: req.AgentType, Label: req.Label, StateTokens: stateTokens, JevModel: c.Meta.JevModel, Warm: req.Warm,
		Signals: req.Signals, Repo: req.RepoRoot,
	}
	if cur != nil {
		rec.From = cur.ID
	}
	if req.Work != nil {
		rec.WorkTier, rec.WorkDone = req.Work.Tier, req.Work.Done
	}
	if req.Paused != nil {
		rec.PausedTier = req.Paused.Tier
	}
	params := policy.Params{Penalty: c.Meta.UnderprovisionPenalty, Scale: req.Scale}
	if params.Penalty <= 0 {
		params.Penalty = 1.5
	}
	keep := func(reason string) (*state.Decision, Outcome) {
		d := *req.Current
		rec.Kept, rec.KeepReason, rec.Chosen, rec.Model, rec.Effort, rec.Mode = true, reason, d.Tier, d.APIID, d.Effort, d.Mode
		rec.LatencyMS = e.Now().Sub(start).Milliseconds()
		if err := e.Ledger.Append(rec); err != nil {
			log.Printf("ledger: %v", err)
		}
		return &d, Outcome{Reason: reason}
	}

	// A warm switch has to pay back its cost: when no answer could, Jev
	// is not asked at all (unless the prompt may ask for something).
	if cur != nil && f.CostAware && req.SwitchCost != nil && req.MinTier == "" && len(req.Explicit) == 0 && !e.AboveCap(req.SessionID, req.Scope, cur) {
		// Modes flip for free but wait for the next free moment then.
		if g := policy.MaxGain(c, req.Scope, cur, req.SwitchCost, params); g <= 0 {
			rec.Skipped = true
			return keep("no switch can pay back its cost")
		}
	}

	e.keepState(rec, req.State)
	timeout := e.Cfg.JevTimeout.Duration
	if req.Warm && f.WarmTimeout.Duration > 0 {
		timeout = f.WarmTimeout.Duration
	}
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var (
		wg       sync.WaitGroup
		shadow   *ledger.Shadow
		shAnswer map[string]jev.Answer
	)
	if m := e.Cfg.JevShadowModel; m != "" && m != c.Meta.JevModel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, resp, err := e.Jev.Ask(ctx, m, req.SessionID, req.State, qs)
			shadow = &ledger.Shadow{Model: m}
			if resp != nil {
				shadow.CostUSD = resp.Usage.Cost
			}
			if err != nil {
				shadow.Error = err.Error()
				return
			}
			shAnswer = a
		}()
	}
	ans, resp, err := e.Jev.Ask(ctx, c.Meta.JevModel, req.SessionID, req.State, qs)
	wg.Wait()
	rec.LatencyMS = e.Now().Sub(start).Milliseconds()
	if resp != nil {
		rec.JevCostUSD = resp.Usage.Cost
	}
	e.noteJev(req.SessionID, err)
	dec := &state.Decision{Scope: req.Scope, Trigger: req.Trigger, DecidedAt: start}
	if err != nil {
		rec.Error = err.Error()
		timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)
		if req.Warm && req.Current != nil {
			log.Printf("jev %s/%s: %v (kept %s)", req.Scope, req.Trigger, err, req.Current.Tier)
			d, out := keep("jev error")
			out.TimedOut = timedOut
			return d, out
		}
		// The default tier, or the work in progress when it needs more
		// (after a compaction or a pause), within the repo's bounds and the
		// budget cap.
		tier, mode := c.DefaultTier(req.Scope), ""
		if req.Work != nil {
			if w := c.Tier(req.Scope, req.Work.Tier); w != nil && w.Rank >= tier.Rank {
				tier, mode = w, req.Work.Mode
			}
		}
		tier, mode = e.bounded(req, tier, mode, rp)
		dec.Trigger, dec.Cause = "fallback", req.Trigger
		rec.Trigger, rec.Cause = "fallback", req.Trigger
		log.Printf("jev %s/%s: %v (default tier %s)", req.Scope, req.Trigger, err, tier.ID)
		e.fill(dec, tier, mode)
		if req.Work != nil {
			e.onModel(dec, req.Work.Model) // the work goes on on its model
		}
		rec.Chosen, rec.Model, rec.Effort, rec.Mode = dec.Tier, dec.APIID, dec.Effort, dec.Mode
		if err := e.Ledger.Append(rec); err != nil {
			log.Printf("ledger: %v", err)
		}
		out := Outcome{Changed: true, TimedOut: timedOut}
		if req.Scope == catalog.ScopeMain && req.Work == nil {
			out.Work = &WorkUpdate{Kind: WorkNew, Tier: dec.Tier, Mode: dec.Mode}
		}
		return dec, out
	}

	rd := e.Read(ans, ids, req.Scope)
	if metadata {
		rd.unconfirmedMore(task, req.Explicit) // the words alone, as before Jev confirmed them
	}
	rec.Probs, rec.Confidence, rec.JevChoice, rec.ModeP, rec.AskedP = rd.probs, rd.conf, rd.top, rd.modeP, rd.asked
	rec.Relation = rd.relation
	for id, p := range rd.explicit {
		if rec.Explicit == nil {
			rec.Explicit = map[string]float64{}
		}
		rec.Explicit[strings.TrimPrefix(id, jev.QExplicitPfx)] = p
	}
	dec.JevChoice, dec.Confidence, dec.Probs = rd.top, rd.conf, rd.probs

	v := e.Judge(req, rd, cur, rp, params)
	v, rec.BudgetCap = e.capBudget(req, v, cur)
	rec.Hold = v.Hold
	if v.Work != nil {
		rec.Work, rec.Pauses = v.Work.Kind, v.Work.Pause
	}
	if v.Pick != nil {
		rec.Loss, rec.GainUSD, rec.SwitchUSD = v.Pick.Loss, v.Pick.Gain, v.Pick.SwitchCost
	}
	if shadow != nil && shAnswer != nil {
		sd := e.Read(shAnswer, ids, req.Scope)
		sv := e.Judge(req, sd, cur, rp, params)
		shadow.Choice, shadow.Confidence, shadow.Probs, shadow.Chosen = sd.top, sd.conf, sd.probs, sv.Tier.ID
	}
	rec.Shadow = shadow
	e.fill(dec, v.Tier, v.Mode)
	e.onModel(dec, v.Model)
	// The switch costs what moving to the model and effort the decision
	// ends up on costs (the work's model, a paused work's, one asked in
	// words), not the tier's.
	if req.Warm && req.Current != nil && req.SwitchCost != nil {
		rec.SwitchUSD = req.SwitchCost(e.asTier(req.Scope, dec.Model, dec.Effort))
	}
	// On the work's model the tier is only a level: the same model, effort
	// and mode is no change.
	if cur := req.Current; v.Keep == "" && v.Model != "" && req.Warm && cur != nil && dec.Model == cur.Model && dec.Effort == cur.Effort && dec.Mode == cur.Mode {
		v.Keep = "same model and effort"
	}
	if v.Keep != "" {
		d, out := keep(v.Keep)
		out.Work = v.Work
		return d, out
	}
	rec.Chosen, rec.Model, rec.Effort, rec.Mode = dec.Tier, dec.APIID, dec.Effort, dec.Mode
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return dec, Outcome{Changed: true, Work: v.Work}
}

// noteJev records on the session why Jev couldn't answer (shown by the
// status line), or clears it once Jev answers again.
func (e *Env) noteJev(sessionID string, err error) {
	issue := JevIssue(err)
	if sessionID == "" {
		return
	}
	e.State.Update(sessionID, func(s *state.Session) bool {
		if s.JevIssue == issue {
			return false
		}
		s.JevIssue, s.JevIssueAt = issue, e.Now()
		return true
	})
}

// JevIssue is a short reason for a failed Jev call ("" when it answered).
func JevIssue(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case errors.Is(err, jev.ErrNoKey):
		return "no OpenRouter key"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "Timeout") || strings.Contains(msg, "deadline"):
		return "timeout"
	case strings.Contains(msg, " 401") || strings.Contains(msg, "status 401") || strings.Contains(msg, "jev: 401"):
		return "OpenRouter key rejected"
	case strings.Contains(msg, "402"):
		return "OpenRouter credits"
	case strings.Contains(msg, "429"):
		return "rate limited"
	}
	return "unreachable"
}

// Verdict is the policy's answer to a reading.
type Verdict struct {
	Tier *catalog.Tier
	Mode string
	Keep string // warm: why the current decision stays
	Pick *policy.Pick
	// Hold says what set the tier besides the policy's pick: the work in
	// progress a follow-up keeps ("follow-up of the work in progress
	// (extend 0.94)", "mid-turn", "peer message"), or what the prompt asked
	// for ("explicit effort xhigh", "ultrathink").
	Hold string
	// Work is what becomes of the work in progress (nil: unchanged).
	Work *WorkUpdate
	// Model is the model the decision runs on when it isn't the tier's:
	// one the prompt asked for in words, else the one the work it follows
	// up runs on ("": the tier's).
	Model string
}

// Judge applies the policy to a reading, in this order: an effort the
// prompt asks for in words (up or down); the thinking floors (ultrathink,
// "think harder"); the prompt's relation to the work in progress, where a
// follow-up keeps at least the work's tier and separate work gets its own
// level, and going back to paused work brings its tier back; the repo's
// bounds; the modes; the model the work runs on; and on warm turns the
// confidence a downgrade that rebuilds the cache needs.
func (e *Env) Judge(req Request, rd Reading, cur *catalog.Tier, rp policy.RepoPolicy, params policy.Params) Verdict {
	c, f := e.Catalog, e.Cfg.Features
	tier, pk := e.pick(req, rd, cur, params)
	if a := e.asked(req, rd, cur, tier, rp); a != nil {
		tier = a
	}
	x := e.confirmed(req, rd)
	// The work the prompt follows up: the work in progress, or the paused
	// work it goes back to.
	followed := req.Work
	back := e.resumes(req, rd)
	if back {
		followed = req.Paused
	}
	var work *catalog.Tier
	if followed != nil {
		work = c.Tier(req.Scope, followed.Tier)
	}
	v := Verdict{Pick: pk}
	var why []string
	hold, reason := e.holdAt(req, rd, work, cur)
	top, _ := rd.relationTop()
	// Separate new work (or a first prompt): its own level, mode and model.
	fresh := work == nil || (hold == nil && top == catalog.RelationNewTask)
	// A wrap-up, a side question or an aside is answered alone: without the
	// work's mode, which stays the work's (Jev's mode answer reads the whole
	// work: "open the draft PR" after a sweep still reads as the sweep).
	alone := thisTurn(top) && !req.MidTurn && !req.Peer
	// The mode a follow-up keeps on: the work's, and on a mid-turn prompt
	// or a peer message the one the turn runs with (only that one once the
	// work is done: the turn runs something else).
	keepMode := ""
	if hold != nil && !alone {
		interjected := req.MidTurn || req.Peer
		if !followed.Done || back || !interjected {
			keepMode = followed.Mode
		}
		if keepMode == "" && interjected && req.Warm && req.Current != nil {
			keepMode = req.Current.Mode
		}
	}
	if x.effort != nil {
		// An effort asked in words is the user's call, up or down: the mode
		// a follow-up keeps stays on unless it would raise that effort (and
		// Jev may still turn on one that doesn't, see mode).
		tier = x.effort
		if keepMode != "" && !c.KeepsMode(keepMode, x.effort.Effort) {
			keepMode = ""
		}
		why = append(why, "explicit effort "+x.effort.Effort)
	} else {
		floor := hold
		if reason != "" {
			why = append(why, reason)
		}
		// A model asked in words for the work as it stands (not with more
		// work to it) keeps the work's level: the words say which model, and
		// Jev's level rates them ("no need for Opus for CSS", "it's the
		// tricky part").
		if x.model != "" && hold != nil && top != catalog.RelationExtend {
			tier = hold
		}
		if t := c.Tier(req.Scope, req.MinTier); t != nil {
			floor = higher(floor, t)
			why = append(why, "ultrathink")
		}
		if x.more {
			// More thinking is one rank above what runs; on a follow-up that
			// is the decision too, since Jev's level rates the words asking
			// for it ("think harder" reads as hard work).
			more := e.above(req.Scope, higher(work, cur))
			if hold != nil {
				tier = more
			}
			floor = higher(floor, more)
			why = append(why, "explicit: more thinking")
		}
		if floor != nil && tier.Rank < floor.Rank {
			tier = floor
		}
	}
	tier = policy.Constrain(c, req.Scope, tier, rp, req.Context) // within the repo's bounds
	mode := ""
	if !alone || x.on != "" {
		mode, tier = e.mode(req, rd, tier, rp, x, keepMode, fresh)
	}
	if mode != "" {
		// The tier the mode needs may be past the repo's bounds.
		if t := policy.Constrain(c, req.Scope, tier, rp, req.Context); t.ID != tier.ID {
			tier = t
			if min := c.Tier(req.Scope, c.Modes[mode].MinTier); min != nil && tier.Rank < min.Rank {
				mode = ""
			}
		}
	}
	switch {
	case x.on != "" && mode == x.on:
		why = append(why, "explicit "+x.on)
	case x.off:
		why = append(why, "explicit: no "+cmp.Or(e.inForceMode(req), "mode"))
	}
	// The model: one asked for in words, else the work's as long as the
	// prompt doesn't start separate new work (a wrap-up stays on it too).
	if !fresh {
		v.Model = followed.Model
	}
	if x.model != "" {
		v.Model = e.offTiers(x.model)
		why = append(why, "explicit model "+x.model)
	}
	v.Tier, v.Mode, v.Hold = tier, mode, strings.Join(why, "; ")
	// The model the work runs on: the gate below only keeps this turn on
	// the model in force.
	workModel := v.Model

	// The model the decision runs on, and what moving there costs.
	to := e.asTier(req.Scope, cmp.Or(v.Model, tier.Model), tier.Effort)
	free := req.SwitchCost == nil || req.SwitchCost(to) <= 0
	if cur == nil && req.Warm && req.Current != nil && !free && x.model == "" && to.Model != req.Current.Model && rd.conf < f.WarmMinConfidence {
		// The decision in force runs off the tiers (a model asked for in
		// words): leaving that model rebuilds the cache, so like a costly
		// downgrade it needs a sure answer; until then the level applies
		// on the same model.
		v.Model = e.offTiers(req.Current.Model)
		name := req.Current.Model
		if m := e.Catalog.Model(name); m != nil {
			name = m.Label
		}
		why = append(why, fmt.Sprintf("confidence %.2f below %.2f: stays on %s", rd.conf, f.WarmMinConfidence, name))
		v.Hold = strings.Join(why, "; ")
	}
	if cur != nil {
		curMode := req.Current.Mode
		// A switch that costs something (a cache rebuild) and lowers the
		// tier on an unsure answer doesn't pay back; an upgrade under doubt
		// is the safe side (the policy already weighed its cost), so it
		// goes through: a session on Haiku must not stay there on hard work
		// because Jev hesitates between high and xhigh. Free switches (per-
		// turn effort) and what the prompt asked for need no confidence.
		switch {
		case tier.ID == cur.ID && mode == curMode && (v.Model == "" || v.Model == req.Current.Model):
			v.Keep = "same tier"
		case free || x.any() || req.MinTier != "":
		case tier.Rank < cur.Rank && rd.conf < f.WarmMinConfidence:
			v.Tier = cur
			if mode == curMode {
				v.Keep = fmt.Sprintf("confidence %.2f below %.2f", rd.conf, f.WarmMinConfidence)
			}
		}
	}
	if req.Scope == catalog.ScopeMain {
		// More work on a done work reopens it (not a prompt typed while
		// Claude runs something else, nor another session's message).
		reopen := followed != nil && followed.Done && hold != nil && !req.MidTurn && !req.Peer &&
			(top == catalog.RelationContinue || top == catalog.RelationExtend || req.FollowUp == FollowUpProposal)
		w := v
		w.Model = workModel
		v.Work = workUpdate(w, followed, work, hold, x, top, fresh, back, reopen)
		// New work below the paused work: a longer detour, the paused work
		// waits on (unless the work it replaces needs more and waits instead).
		if u := v.Work; u != nil && u.Kind == WorkNew && req.Paused != nil {
			if p := c.Tier(req.Scope, req.Paused.Tier); p != nil && v.Tier.Rank < p.Rank && (!u.Pause || e.HigherWork(req.Work, req.Paused) == req.Paused) {
				u.Pause, u.KeepPaused = false, true
			}
		}
		// More thinking read from the words alone raises this turn only:
		// the work in progress is what it would be without it.
		if x.more && rd.guessedMore {
			without := rd
			without.guessedMore, without.explicit = false, maps.Clone(rd.explicit)
			delete(without.explicit, jev.Explicit{Kind: jev.ExplicitEffort, Value: jev.ExplicitMore}.ID())
			v.Work = e.Judge(req, without, cur, rp, params).Work
		}
	}
	return v
}

// workUpdate is what a verdict makes of the work in progress: separate new
// work (or a first prompt) starts it, pausing the work in progress when
// the new work is below it (a detour, unless that work is done); going
// back to the paused work restores it; an effort, a mode or a model asked
// in words sets it; a follow-up that needs more raises it; a wrap-up marks
// it done, and more work on it (reopen) opens it again; a side question,
// an aside or a plain follow-up leave it as it is. What a wrap-up, a side
// question or an aside asks for (an effort, more thinking, a mode, a
// model) or needs (ultrathink, a level above the work) is for that answer
// only, and so is anything a prompt that takes its own level asks for (top:
// the prompt's likeliest relation). v.Model is the model the work runs on.
func workUpdate(v Verdict, followed *state.Work, work, hold *catalog.Tier, x asks, top string, fresh, back, reopen bool) *WorkUpdate {
	switch {
	case fresh:
		u := &WorkUpdate{Kind: WorkNew, Tier: v.Tier.ID, Mode: v.Mode, Model: v.Model}
		u.Pause = work != nil && !followed.Done && (v.Tier.Rank < work.Rank || (v.Tier.Rank == work.Rank && followed.Mode != "" && v.Mode == ""))
		return u
	case back:
		return &WorkUpdate{Kind: WorkResumed, Tier: v.Tier.ID, Mode: v.Mode, Model: v.Model}
	}
	switch {
	case hold == nil && top == catalog.RelationWrapUp && !followed.Done:
		return &WorkUpdate{Kind: WorkDone, Tier: work.ID, Mode: followed.Mode, Model: followed.Model, Done: true}
	case thisTurn(top), hold == nil:
		return nil
	case x.effort != nil || x.on != "" || x.off:
		return &WorkUpdate{Kind: WorkSet, Tier: v.Tier.ID, Mode: v.Mode, Model: v.Model}
	}
	// A follow-up: the work's tier and mode, or the verdict's when they
	// need more.
	u := &WorkUpdate{Tier: higher(work, v.Tier).ID, Mode: cmp.Or(v.Mode, followed.Mode), Model: v.Model}
	switch {
	case v.Model != followed.Model:
		u.Kind = WorkSet
	case v.Tier.Rank > work.Rank || (v.Mode != "" && v.Mode != followed.Mode):
		u.Kind = WorkRaised
	case reopen:
		u.Kind = WorkReopened
	default:
		return nil
	}
	return u
}

// thisTurn reports a relation answered for that turn only: a wrap-up, a
// side question or an aside.
func thisTurn(relation string) bool {
	return relation == catalog.RelationWrapUp || relation == catalog.RelationSideQuestion || relation == catalog.RelationAside
}

// inForceMode is the mode of the decision in force: the current one on a
// warm turn, else the work in progress's (after a compaction or a pause).
func (e *Env) inForceMode(req Request) string {
	switch {
	case req.Warm && req.Current != nil:
		return req.Current.Mode
	case req.Work != nil:
		return req.Work.Mode
	}
	return ""
}

// asked returns the asked tier that replaces tier, if Jev says yes to its
// question and it fits the context and the repo's bounds. An asked tier on
// another model is taken when there is no cache to lose (a new session,
// after /compact or a pause) or when the session is already on it; a warm
// session is never moved there: with the cache warm, a turn on Haiku costs
// about what a turn on Opus low does (cache reads dominate), and coming
// back rebuilds the whole context.
func (e *Env) asked(req Request, rd Reading, cur, tier *catalog.Tier, rp policy.RepoPolicy) *catalog.Tier {
	c := e.Catalog
	a := c.AskedAbove(req.Scope, tier)
	if a == nil || rd.asked[a.ID] < a.Threshold || !c.Fits(a, req.Context) || policy.Constrain(c, req.Scope, a, rp, req.Context).ID != a.ID {
		return nil
	}
	if req.Warm && cur != nil && cur.ID != a.ID && cur.Model != a.Model {
		return nil
	}
	return a
}

// Reading is Jev's answer mapped onto the catalog.
type Reading struct {
	probs    map[string]float64
	conf     float64
	top      string
	modeP    map[string]float64
	relation map[string]float64 // the prompt's relation to the work in progress (and to paused work)
	explicit map[string]float64 // explicit requests: yes-probability by question ID
	asked    map[string]float64 // asked tiers: Jev's yes-probability
	// guessedMore: more thinking counted from the words alone (metadata
	// privacy), without Jev's confirmation.
	guessedMore bool
}

// separate is the probability that the prompt is separate from the work in
// progress (a new task, a wrap-up or an aside).
func (rd Reading) separate() float64 {
	var p float64
	for r, q := range rd.relation {
		if catalog.Separate(r) {
			p += q
		}
	}
	return p
}

// relationTop is the most likely relation and its probability.
func (rd Reading) relationTop() (string, float64) {
	top, bp := "", 0.0
	for _, r := range catalog.Relations {
		if p := rd.relation[r]; p > bp {
			top, bp = r, p
		}
	}
	return top, bp
}

func (e *Env) Read(ans map[string]jev.Answer, ids []string, scope string) Reading {
	lv := ans[jev.QLevel]
	rd := Reading{probs: jev.LevelProbs(lv, ids), conf: lv.Confidence}
	bp := -1.0
	for _, id := range ids {
		if rd.probs[id] > bp {
			rd.top, bp = id, rd.probs[id]
		}
	}
	for _, m := range e.Catalog.ModesFor(scope) {
		if a, ok := ans[jev.QModePfx+m.ID]; ok && a.Noul != nil {
			if rd.modeP == nil {
				rd.modeP = map[string]float64{}
			}
			rd.modeP[m.ID] = *a.Noul
		}
	}
	if a, ok := ans[jev.QRelation]; ok && len(a.Probabilities) > 0 {
		rd.relation = a.Probabilities
	}
	for id, a := range ans {
		switch {
		case a.Noul == nil:
		case strings.HasPrefix(id, jev.QTierPfx):
			if rd.asked == nil {
				rd.asked = map[string]float64{}
			}
			rd.asked[strings.TrimPrefix(id, jev.QTierPfx)] = *a.Noul
		case strings.HasPrefix(id, jev.QExplicitPfx):
			if rd.explicit == nil {
				rd.explicit = map[string]float64{}
			}
			rd.explicit[id] = *a.Noul
		}
	}
	return rd
}

// pick applies the cost-aware policy (or the v1 confidence rule).
func (e *Env) pick(req Request, rd Reading, cur *catalog.Tier, params policy.Params) (*catalog.Tier, *policy.Pick) {
	c := e.Catalog
	if e.Cfg.Features.CostAware {
		sc := req.SwitchCost
		if sc == nil {
			sc = func(*catalog.Tier) float64 { return 0 }
		}
		if params.Scale <= 0 {
			params.Scale = 1
		}
		pk := policy.Best(c, req.Scope, rd.probs, cur, sc, params)
		return pk.Tier, &pk
	}
	th := policy.Thresholds{Act: e.Cfg.ThetaAct, Low: e.Cfg.ThetaLow}
	return policy.Choose(c, req.Scope, &jev.Answer{Choice: rd.top, Confidence: rd.conf, Probabilities: rd.probs}, th), nil
}

// mode decides the mode layered on the tier (one at most is used) and the
// tier it runs at. A mode asked for or refused in words wins; a follow-up
// keeps the work in progress's mode on (keep); otherwise Jev's answer
// decides, except for a mode that would raise an effort asked in words,
// and a mode in force only turns off on a clear no, unless the prompt
// starts separate new work (fresh: a small new task in an ultracode
// session gets no mode). A mode that ends up on raises the tier to its
// min_tier and to the tier its effort runs as, so the tier stored says
// what runs.
func (e *Env) mode(req Request, rd Reading, t *catalog.Tier, rp policy.RepoPolicy, x asks, keep string, fresh bool) (string, *catalog.Tier) {
	c := e.Catalog
	allowed := func(id string) bool {
		for _, m := range c.ModesFor(req.Scope) {
			if m.ID == id {
				return rp.ModeAllowed(id)
			}
		}
		return false
	}
	switch {
	case x.off:
		return "", t
	case x.on != "" && allowed(x.on):
		return x.on, e.modeTier(req.Scope, t, x.on)
	case keep != "" && allowed(keep):
		return keep, e.modeTier(req.Scope, t, keep)
	}
	inForce := e.inForceMode(req)
	for _, m := range c.ModesFor(req.Scope) {
		p, ok := rd.modeP[m.ID]
		if !ok || !rp.ModeAllowed(m.ID) || (x.effort != nil && !c.KeepsMode(m.ID, x.effort.Effort)) {
			continue
		}
		if inForce == m.ID && !fresh {
			if p > 1-m.Threshold { // turning off needs a clear no
				return m.ID, e.modeTier(req.Scope, t, m.ID)
			}
			continue
		}
		if min := c.Tier(req.Scope, m.MinTier); p >= m.Threshold && (min == nil || t.Rank >= min.Rank) {
			return m.ID, e.modeTier(req.Scope, t, m.ID)
		}
	}
	return "", t
}

// modeTier is the tier a mode runs at on top of t: at least its min_tier,
// and the tier running the effort the mode raises t's to (ultracode on a
// low tier runs at xhigh, and the decision says xhigh).
func (e *Env) modeTier(scope string, t *catalog.Tier, modeID string) *catalog.Tier {
	c := e.Catalog
	if min := c.Tier(scope, c.Modes[modeID].MinTier); min != nil && t.Rank < min.Rank {
		t = min
	}
	model, effort, _ := c.Resolve(t, modeID)
	if rt := c.TierFor(scope, model, effort); rt != nil && rt.Rank > t.Rank {
		t = rt
	}
	return t
}

// Pinned is the decision for an effort the user chose. It is logged like
// any decision, with the source (/effort or prompt) as cause. The mode in
// force stays on unless the pinned effort is below its own (the mode would
// raise it).
func (e *Env) Pinned(sessionID, repoRoot string, t *catalog.Tier, mode, source string) *state.Decision {
	if !e.Catalog.KeepsMode(mode, t.Effort) {
		mode = ""
	}
	now := e.Now()
	d := &state.Decision{Scope: catalog.ScopeMain, Trigger: "pinned", Cause: source, DecidedAt: now, Confidence: 1}
	e.fill(d, t, mode)
	rec := ledger.Decision{TS: now, Kind: "decision", SessionID: sessionID, Scope: catalog.ScopeMain, Trigger: "pinned",
		Cause: source, Chosen: d.Tier, Model: d.APIID, Effort: d.Effort, Mode: d.Mode, Confidence: 1, Repo: repoRoot}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return d
}

// PinnedModel is the decision for a model the user pinned ([model:X]),
// at effort. A model and effort that a main tier runs are pinned as that
// tier; any other pair keeps state.PinnedTier. The mode in force stays as
// for Pinned.
func (e *Env) PinnedModel(sessionID, repoRoot, model, effort, mode, source string) *state.Decision {
	if t := e.Catalog.TierFor(catalog.ScopeMain, model, effort); t != nil {
		return e.Pinned(sessionID, repoRoot, t, mode, source)
	}
	now := e.Now()
	m := e.Catalog.Model(model)
	d := &state.Decision{Scope: catalog.ScopeMain, Tier: state.PinnedTier, Model: model, APIID: m.APIID, Effort: effort,
		Trigger: "pinned", Cause: source, DecidedAt: now, Confidence: 1}
	if e.Catalog.KeepsMode(mode, effort) {
		d.Mode, d.Workflows = mode, e.Catalog.Modes[mode].Workflows
	}
	rec := ledger.Decision{TS: now, Kind: "decision", SessionID: sessionID, Scope: catalog.ScopeMain, Trigger: "pinned",
		Cause: source, Chosen: d.Tier, Model: d.APIID, Effort: d.Effort, Mode: d.Mode, Confidence: 1, Repo: repoRoot}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return d
}

// Carried is what a bare go-ahead does, without asking Jev (which rates
// the bare word as trivial): it carries on the work in progress, or the
// paused work when that needs more (the detour is over), at the tier, mode
// and model it was decided at (a side question since may have lowered the
// decision in force), within the repo's bounds (rp) and the budget cap. A
// work a wrap-up closed is not carried unless the paused work needs more
// (Acknowledges: the hooks route the prompt). On a
// warm cache it doesn't move to another model: that rebuilds the whole
// context (the asked Haiku tier, see asked). It returns the decision (a
// copy of req.Current when nothing changes) and what becomes of the work.
func (e *Env) Carried(req Request, rp policy.RepoPolicy) (*state.Decision, *WorkUpdate) {
	cur := req.Current
	d := *cur
	w, resumed := e.GoAheadWork(cur, req.Work, req.Paused, req.MidTurn)
	if w == nil {
		return &d, nil
	}
	var u *WorkUpdate
	if resumed {
		u = &WorkUpdate{Kind: WorkResumed, Tier: w.Tier, Mode: w.Mode, Model: w.Model}
	}
	if t := e.Catalog.Tier(catalog.ScopeMain, w.Tier); t != nil {
		t, mode := e.bounded(req, t, w.Mode, rp)
		c := d
		e.fill(&c, t, mode)
		e.onModel(&c, w.Model)
		if req.Trigger != "warm" || c.Model == cur.Model {
			d = c
		}
	}
	return &d, u
}

// Carry applies Carried. At a moment that would otherwise be decided
// (compaction, cold cache) it is logged like a decision, with the go-ahead
// as reason; on a warm turn that changes nothing it is only logged as
// kept, and the decision returned is nil.
func (e *Env) Carry(req Request) (*state.Decision, *WorkUpdate) {
	cur := req.Current
	d, u := e.Carried(req, policy.LoadRepoPolicy(req.RepoDir, e.Cfg.RepoPolicyFile))
	same := d.Tier == cur.Tier && d.Model == cur.Model && d.Effort == cur.Effort && d.Mode == cur.Mode
	d.Trigger, d.Cause, d.DecidedAt = req.Trigger, "go-ahead", e.Now()
	rec := ledger.Decision{TS: d.DecidedAt, Kind: "decision", SessionID: req.SessionID, Scope: cur.Scope, Trigger: req.Trigger,
		Warm: req.Trigger == "warm", From: cur.Tier, Kept: same, Skipped: true,
		Chosen: d.Tier, Model: d.APIID, Effort: d.Effort, Mode: d.Mode}
	if w := cmp.Or(req.Work, req.Paused); w != nil {
		rec.WorkTier = w.Tier
	}
	if req.Paused != nil {
		rec.PausedTier = req.Paused.Tier
	}
	if u != nil {
		rec.Work = u.Kind
	}
	if same {
		rec.KeepReason = "go-ahead: continues the work in progress"
	} else {
		rec.Hold = "go-ahead: back to the work in progress"
	}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	if req.Trigger == "warm" && same {
		return nil, u
	}
	return d, u
}

// DefaultDecision is the tier applied when nothing was decided.
func (e *Env) DefaultDecision(scope, trigger string) *state.Decision {
	d := &state.Decision{Scope: scope, Trigger: trigger, DecidedAt: e.Now()}
	e.fill(d, e.Catalog.DefaultTier(scope), "")
	return d
}

func (e *Env) fill(d *state.Decision, t *catalog.Tier, mode string) {
	model, effort, wf := e.Catalog.Resolve(t, mode)
	m := e.Catalog.Model(model)
	d.Tier, d.Model, d.APIID, d.Effort, d.Workflows, d.Mode = t.ID, model, m.APIID, effort, wf, mode
}

// onModel moves a decision onto the model its work runs on, when that
// isn't the tier's (a model asked for in words; "" leaves it): at the
// tier's effort if the model has it, else the model's default. The tier
// becomes the model's tier at that effort, or state.PinnedTier for a model
// outside the tiers (the proxy then sends its model and effort as they
// are). The mode stays unless the effort is below its own.
func (e *Env) onModel(d *state.Decision, model string) {
	c := e.Catalog
	m := c.Model(model)
	if m == nil || model == d.Model {
		return
	}
	effort := d.Effort
	if !m.SupportsEffort(effort) {
		effort = m.DefaultEffort
	}
	d.Tier = state.PinnedTier
	if t := c.TierFor(catalog.ScopeMain, model, effort); t != nil {
		d.Tier = t.ID
	}
	d.Model, d.APIID, d.Effort = model, m.APIID, effort
	if !c.KeepsMode(d.Mode, effort) {
		d.Mode, d.Workflows = "", false
	}
}

// asTier is the tier running model at effort, for switch costs: the
// scope's tier if one runs them, else one outside the catalog's (a model
// asked for in words).
func (e *Env) asTier(scope, model, effort string) *catalog.Tier {
	if t := e.Catalog.TierFor(scope, model, effort); t != nil {
		return t
	}
	return &catalog.Tier{ID: state.PinnedTier, Model: model, Effort: effort}
}

// offTiers is the model a work asked to run on in words runs on besides
// the tiers': "" for the tiers' own model (asking for it goes back to
// routing on the tiers).
func (e *Env) offTiers(model string) string {
	if model == e.Catalog.DefaultTier(catalog.ScopeMain).Model {
		return ""
	}
	return model
}

// Budget is the token budget for the state, after the questions.
func (e *Env) Budget(scope string) int {
	qs, _ := jev.Questions(e.Catalog, scope, jev.Ask{Relation: true, Resume: true})
	return e.Cfg.StateBudgetTokens - tokens.Estimate(mustJSON(qs)) - 200
}

// FitRepo trims repo signals into the remaining budget, keeping the diff
// stat before the CLAUDE.md head (spec §6.2: task > diff_stat > claude_md).
func FitRepo(r *state.RepoSignals, budget int) map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{"languages": r.Languages, "files": r.Files}
	budget -= 50
	if r.DiffStat != "" && budget > 50 {
		ds := tokens.Truncate(r.DiffStat, budget)
		out["diff_stat"] = ds
		budget -= tokens.Estimate(ds)
	}
	if r.ClaudeMDHead != "" && budget > 100 {
		out["claude_md_head"] = tokens.Truncate(r.ClaudeMDHead, budget)
	}
	return out
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Logf appends to the hooks log (hooks must stay silent on stdout/stderr).
func SetupLog(stateDir, name string) func() {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return func() {}
	}
	f, err := os.OpenFile(filepath.Join(stateDir, name), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}
	}
	log.SetOutput(f)
	log.SetPrefix(fmt.Sprintf("[%d] ", os.Getpid()))
	return func() { f.Close() }
}
