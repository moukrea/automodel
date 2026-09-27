// Package router is the decision engine shared by the hooks: it builds the
// Jev state, calls Jev (and the shadow model), applies the policy and records
// the decision in the ledger.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/jev"
	"github.com/moukrea/automodel/internal/ledger"
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
	store := &catalog.Store{Path: cfg.Catalog, LastGood: cfg.LastGoodCatalog(), StaleDays: cfg.StaleDays}
	cat, err := store.Get()
	if err != nil {
		return nil, err
	}
	return &Env{
		Cfg:     cfg,
		Catalog: cat,
		State:   state.Store{Dir: cfg.StateDir},
		Ledger:  ledger.Ledger{Path: cfg.Ledger},
		States:  statesFor(cfg),
		Jev:     &jev.Client{URL: cfg.JevURL, APIKey: cfg.APIKey()},
		Now:     time.Now,
	}, nil
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
	// Signals the user gave (interrupted turn, asked for more thinking);
	// MinTier is a floor they imply.
	Signals map[string]any
	MinTier string

	Warm       bool
	Current    *state.Decision
	SwitchCost func(*catalog.Tier) float64
	Scale      float64
}

// Outcome says what a warm decision did.
type Outcome struct {
	Changed bool
	Reason  string // why a warm decision kept the current tier
}

// Decide asks Jev (and the shadow model, if configured) and applies the
// policy. It never fails: on errors a warm turn keeps its tier and other
// triggers get the scope's default tier (trigger "fallback"). The decision
// is appended to the ledger.
func (e *Env) Decide(ctx context.Context, req Request) (*state.Decision, Outcome) {
	c := e.Catalog
	f := e.Cfg.Features
	rp := policy.LoadRepoPolicy(req.RepoDir, e.Cfg.RepoPolicyFile)
	// A repository can only make privacy stricter, never looser.
	if rp.Privacy == PrivacyMetadata || e.Cfg.Privacy == PrivacyMetadata {
		req.State = MetadataOnly(req.State)
	}
	qs, ids := jev.Questions(c, req.Scope, req.Warm)
	stateTokens := tokens.Estimate(mustJSON(req.State))
	start := e.Now()
	var cur *catalog.Tier
	if req.Warm && req.Current != nil {
		cur = c.Tier(req.Scope, req.Current.Tier)
	}
	rec := ledger.Decision{
		TS: start, Kind: "decision", ID: ledger.NewID(), SessionID: req.SessionID, Scope: req.Scope, Trigger: req.Trigger,
		AgentType: req.AgentType, StateTokens: stateTokens, JevModel: c.Meta.JevModel, Warm: req.Warm,
		Signals: req.Signals, Repo: req.RepoRoot,
	}
	if cur != nil {
		rec.From = cur.ID
	}
	params := policy.Params{Penalty: c.Meta.UnderprovisionPenalty, Scale: req.Scale}
	if params.Penalty <= 0 {
		params.Penalty = 3
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
	// is not asked at all.
	if cur != nil && f.CostAware && req.SwitchCost != nil && req.MinTier == "" && !e.AboveCap(req.SessionID, req.Scope, cur) {
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
		if cur != nil {
			log.Printf("jev %s/%s: %v (kept %s)", req.Scope, req.Trigger, err, cur.ID)
			return keep("jev error")
		}
		tier := e.capTier(req, policy.Constrain(c, req.Scope, c.DefaultTier(req.Scope), rp, req.Context))
		dec.Trigger, dec.Cause = "fallback", req.Trigger
		rec.Trigger, rec.Cause = "fallback", req.Trigger
		log.Printf("jev %s/%s: %v (default tier %s)", req.Scope, req.Trigger, err, tier.ID)
		e.fill(dec, tier, "")
		rec.Chosen, rec.Model, rec.Effort = dec.Tier, dec.APIID, dec.Effort
		if err := e.Ledger.Append(rec); err != nil {
			log.Printf("ledger: %v", err)
		}
		return dec, Outcome{Changed: true}
	}

	rd := e.Read(ans, ids, req.Scope)
	rec.Probs, rec.Confidence, rec.JevChoice, rec.ModeP, rec.ContinuesP = rd.probs, rd.conf, rd.top, rd.modeP, rd.continues
	dec.JevChoice, dec.Confidence, dec.Probs = rd.top, rd.conf, rd.probs

	v := e.Judge(req, rd, cur, rp, params)
	v, rec.BudgetCap = e.capBudget(req, v, cur)
	if v.Pick != nil {
		rec.Loss, rec.GainUSD, rec.SwitchUSD = v.Pick.Loss, v.Pick.Gain, v.Pick.SwitchCost
	}
	if shadow != nil && shAnswer != nil {
		sd := e.Read(shAnswer, ids, req.Scope)
		sv := e.Judge(req, sd, cur, rp, params)
		shadow.Choice, shadow.Confidence, shadow.Probs, shadow.Chosen = sd.top, sd.conf, sd.probs, sv.Tier.ID
	}
	rec.Shadow = shadow
	if v.Keep != "" {
		return keep(v.Keep)
	}
	tier, mode := v.Tier, v.Mode
	e.fill(dec, tier, mode)
	rec.Chosen, rec.Model, rec.Effort, rec.Mode = dec.Tier, dec.APIID, dec.Effort, dec.Mode
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return dec, Outcome{Changed: true}
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
}

// Judge applies the policy to a reading: the tier (cost-aware or v1 rule),
// repo and context constraints, the modes, and on warm turns the gates a
// switch must clear (confidence, no downgrade while the work continues).
func (e *Env) Judge(req Request, rd Reading, cur *catalog.Tier, rp policy.RepoPolicy, params policy.Params) Verdict {
	c, f := e.Catalog, e.Cfg.Features
	tier, pk := e.pick(req, rd, cur, params)
	floor := false
	if min := c.Tier(req.Scope, req.MinTier); min != nil && tier.Rank < min.Rank {
		tier, floor = min, true // the user asked for more thinking...
	}
	tier = policy.Constrain(c, req.Scope, tier, rp, req.Context) // ...within the repo's bounds
	mode := e.mode(req, rd, tier, rp)
	v := Verdict{Tier: tier, Mode: mode, Pick: pk}
	if cur == nil || floor {
		return v // the user asked for it: no warm gate
	}
	curMode := req.Current.Mode
	cont := rd.continues != nil && *rd.continues >= c.Meta.ContinuesThreshold()
	switch {
	case tier.ID == cur.ID && mode == curMode:
		v.Keep = "same tier"
	case tier.ID != cur.ID && rd.conf < f.WarmMinConfidence:
		v.Tier = cur
		if mode == curMode {
			v.Keep = fmt.Sprintf("confidence %.2f below %.2f", rd.conf, f.WarmMinConfidence)
		}
	case tier.Rank < cur.Rank && cont:
		v.Tier = cur
		if mode == curMode {
			v.Keep = "continues the work in progress: no downgrade"
		}
	}
	return v
}

// Reading is Jev's answer mapped onto the catalog.
type Reading struct {
	probs     map[string]float64
	conf      float64
	top       string
	modeP     map[string]float64
	continues *float64
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
	if a, ok := ans[jev.QContinues]; ok && a.Noul != nil {
		v := *a.Noul
		rd.continues = &v
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

// mode decides the modes layered on the tier (one at most is used). On a
// warm turn a mode only flips with a clear answer, and a prompt that
// continues the work in progress keeps the current mode.
func (e *Env) mode(req Request, rd Reading, t *catalog.Tier, rp policy.RepoPolicy) string {
	cur := ""
	if req.Warm && req.Current != nil {
		cur = req.Current.Mode
	}
	if req.Warm && rd.continues != nil && *rd.continues >= e.Catalog.Meta.ContinuesThreshold() {
		return cur
	}
	for _, m := range e.Catalog.ModesFor(req.Scope) {
		p, ok := rd.modeP[m.ID]
		if !ok || !rp.ModeAllowed(m.ID) {
			continue
		}
		if min := e.Catalog.Tier(req.Scope, m.MinTier); min != nil && t.Rank < min.Rank {
			continue
		}
		on := p >= m.Threshold
		if req.Warm && cur == m.ID {
			on = p > 1-m.Threshold // turning off needs a clear no
		}
		if on {
			return m.ID
		}
	}
	return ""
}

// Pinned is the decision for an effort the user chose. It is logged like
// any decision, with the source (/effort or prompt) as cause.
func (e *Env) Pinned(sessionID, repoRoot string, t *catalog.Tier, source string) *state.Decision {
	now := e.Now()
	d := &state.Decision{Scope: catalog.ScopeMain, Trigger: "pinned", Cause: source, DecidedAt: now, Confidence: 1}
	e.fill(d, t, "")
	rec := ledger.Decision{TS: now, Kind: "decision", SessionID: sessionID, Scope: catalog.ScopeMain, Trigger: "pinned",
		Cause: source, Chosen: d.Tier, Model: d.APIID, Effort: d.Effort, Confidence: 1, Repo: repoRoot}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return d
}

// PinnedModel is the decision for a model the user pinned ([model:X]),
// at effort. A model and effort that a main tier runs are pinned as that
// tier; any other pair keeps state.PinnedTier.
func (e *Env) PinnedModel(sessionID, repoRoot, model, effort, source string) *state.Decision {
	if t := e.Catalog.TierFor(catalog.ScopeMain, model, effort); t != nil {
		return e.Pinned(sessionID, repoRoot, t, source)
	}
	now := e.Now()
	m := e.Catalog.Model(model)
	d := &state.Decision{Scope: catalog.ScopeMain, Tier: state.PinnedTier, Model: model, APIID: m.APIID, Effort: effort,
		Trigger: "pinned", Cause: source, DecidedAt: now, Confidence: 1}
	rec := ledger.Decision{TS: now, Kind: "decision", SessionID: sessionID, Scope: catalog.ScopeMain, Trigger: "pinned",
		Cause: source, Chosen: d.Tier, Model: d.APIID, Effort: d.Effort, Confidence: 1, Repo: repoRoot}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
	return d
}

// LogKept records a warm turn that kept the current decision without
// asking Jev.
func (e *Env) LogKept(sessionID string, cur *state.Decision, reason string) {
	if cur == nil {
		return
	}
	rec := ledger.Decision{TS: e.Now(), Kind: "decision", SessionID: sessionID, Scope: cur.Scope, Trigger: "warm",
		Warm: true, From: cur.Tier, Kept: true, KeepReason: reason, Skipped: true,
		Chosen: cur.Tier, Model: cur.APIID, Effort: cur.Effort, Mode: cur.Mode}
	if err := e.Ledger.Append(rec); err != nil {
		log.Printf("ledger: %v", err)
	}
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

// Budget is the token budget for the state, after the questions.
func (e *Env) Budget(scope string) int {
	qs, _ := jev.Questions(e.Catalog, scope, true)
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
