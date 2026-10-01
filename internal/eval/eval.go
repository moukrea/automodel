// Package eval measures Jev's routing answers on labeled cases: accuracy,
// confidence and calibration for the tier level, the modes, the prompt's
// relation to the work in progress and the requests made in words. The
// refresh-model-catalog skill runs it after any change to the criteria,
// the questions or the Jev version.
package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/transcript"
)

type Case struct {
	ID     string          `json:"id"`
	Scope  string          `json:"scope"`
	Warm   bool            `json:"warm"`
	State  map[string]any  `json:"state"`
	Want   string          `json:"want"`
	Accept []string        `json:"accept"`
	Modes  map[string]bool `json:"modes"`
	// Relation labels how the prompt relates to the work in progress (one
	// of catalog.Relations); Explicit what it asks for in words. The work
	// in progress is state.work_in_progress ({goal, level, mode, model,
	// done: a wrap-up closed it}), else the decision in state.current;
	// state.paused_work the work a detour paused; Jev sees their goal,
	// level and done, as the hooks send them (not a paused work marked
	// kept: a detour a go-ahead went back from, which the hooks don't send).
	// state.mid_turn marks a prompt typed while Claude worked (it is not
	// sent to Jev).
	Relation string    `json:"relation,omitempty"`
	Explicit *Explicit `json:"explicit,omitempty"`
	// Split is "train" (criteria and rules may be tuned on it) or "test"
	// (held out: only measured). Note says why the label is right.
	Split string `json:"split,omitempty"`
	Note  string `json:"note,omitempty"`
}

// Explicit labels what a prompt explicitly asks for: an effort ("more":
// more thinking), a mode ("off": no mode), a model (alias or catalog key).
// A case without it asks for nothing.
type Explicit struct {
	Effort string `json:"effort,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Model  string `json:"model,omitempty"`
}

// requests are the explicit-request IDs (effort_xhigh, mode_off...) a label
// asks for, on a session running model. Only another model a main session
// can run on is a model request: Haiku is the asked tier's, and the
// session's own model changes nothing.
func (x *Explicit) requests(cat *catalog.Catalog, model string) []string {
	if x == nil {
		return nil
	}
	var out []string
	if x.Effort != "" {
		out = append(out, jev.ExplicitEffort+"_"+x.Effort)
	}
	if x.Mode != "" {
		out = append(out, jev.ExplicitMode+"_"+x.Mode)
	}
	if key := modelKey(cat, x.Model); key != "" && key != model {
		out = append(out, jev.ExplicitModel+"_"+key)
	}
	return out
}

// sessionModel is the model a main case's session runs on: its current
// tier's, else the model state.current names (a work asked to run on a
// model outside the tiers), else the default tier's.
func (c Case) sessionModel(cat *catalog.Catalog) string {
	cs, _ := c.State["current"].(map[string]any)
	tier, _ := cs["tier"].(string)
	name, _ := cs["model"].(string)
	if t := cat.Tier(catalog.ScopeMain, tier); t != nil {
		return t.Model
	}
	if key := modelKey(cat, name); key != "" {
		return key
	}
	return cat.DefaultTier(catalog.ScopeMain).Model
}

// modelKey is the catalog model a case names: its key, alias, API ID or label.
func modelKey(cat *catalog.Catalog, name string) string {
	if name == "" {
		return ""
	}
	if key := router.MainModel(cat, strings.ToLower(name)); key != "" {
		return key
	}
	for key, m := range cat.Models {
		if strings.EqualFold(m.Label, name) {
			return key
		}
	}
	return ""
}

// holds reports a case whose decision must not fall below the work it
// follows up: a follow-up relation (resume included), a prompt typed
// mid-turn, a message from another session; once a wrap-up closed the
// work, only more work on it (continue, extend, resume).
func (c Case) holds() bool {
	task, _ := c.State["task"].(string)
	if wip, _ := c.State["work_in_progress"].(map[string]any); wip["done"] == true {
		return c.Relation == catalog.RelationContinue || c.Relation == catalog.RelationExtend || c.Relation == catalog.RelationResume
	}
	return FollowUp(c.Relation) || c.State["mid_turn"] == true || transcript.IsPeer(task)
}

// Filter keeps the cases of a split ("" or "all" keeps every case; a case
// without a split is a train case).
func Filter(cs []Case, split string) []Case {
	if split == "" || split == "all" {
		return cs
	}
	var out []Case
	for _, c := range cs {
		s := c.Split
		if s == "" {
			s = "train"
		}
		if s == split {
			out = append(out, c)
		}
	}
	return out
}

type Result struct {
	Case
	Run int    `json:"run,omitempty"`
	Got string `json:"got"`
	// Decision is what the router does with the answer (policy, the work
	// in progress, requests, modes); Kept says why a warm case stays on its
	// current tier, Hold what set the tier besides the pick.
	// Model is the model the decision runs on besides the tier's (the
	// work's, or one asked in words); PausedTier the paused work's tier.
	Decision   string             `json:"decision"`
	Mode       string             `json:"mode,omitempty"`
	Model      string             `json:"model,omitempty"`
	Kept       string             `json:"kept,omitempty"`
	Hold       string             `json:"hold,omitempty"`
	WorkTier   string             `json:"work_tier,omitempty"`
	PausedTier string             `json:"paused_tier,omitempty"`
	Probs      map[string]float64 `json:"probs"`
	Conf       float64            `json:"confidence"`
	ModeP      map[string]float64 `json:"mode_p,omitempty"`
	RelP       map[string]float64 `json:"relation_p,omitempty"`
	RelConf    float64            `json:"relation_confidence,omitempty"`
	// ExplicitP is Jev's yes-probability per request a regex found in the
	// prompt (effort_xhigh, mode_off...).
	ExplicitP map[string]float64 `json:"explicit_p,omitempty"`
	// OfferP: a go-ahead after a detour whose paused work needs more, Jev's
	// yes-probability that the assistant offered one more thing for the
	// detour (router.Request.BackFirst).
	OfferP *float64 `json:"offer_p,omitempty"`
	// FastPath is how the hooks took the prompt without the relation
	// question (fastPath): RelP is then asked apart, only to diagnose; the
	// decision and the relation metrics don't use it.
	FastPath string             `json:"fast_path,omitempty"`
	AskedP   map[string]float64 `json:"asked_p,omitempty"`
	Cost     float64            `json:"cost_usd"`
	Err      string             `json:"error,omitempty"`
}

// Load reads JSONL cases.
func Load(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, path)
}

// Parse reads JSONL cases from r (path is only used in errors).
func Parse(r io.Reader, path string) ([]Case, error) {
	var out []Case
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// Run asks Jev every case, repeat times (Jev's answers vary a little from
// one call to the next). format is "score" (the router's questions) or
// "choice" (the v1 tier Choice, for comparison).
func Run(ctx context.Context, env *router.Env, cases []Case, format string, parallel, repeat int) []Result {
	repeat = max(1, repeat)
	out := make([]Result, len(cases)*repeat)
	sem := make(chan struct{}, max(1, parallel))
	var wg sync.WaitGroup
	for k := 0; k < repeat; k++ {
		for i, c := range cases {
			wg.Add(1)
			go func(j, k int, c Case) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				out[j] = one(ctx, env, c, format)
				out[j].Run = k
			}(k*len(cases)+i, k, c)
		}
	}
	wg.Wait()
	return out
}

// setup is what the hooks would send and judge for a case: the state Jev
// sees, and the router request (the work in progress, the paused work, the
// prompt's requests, mid-turn and peer flags).
func setup(cat *catalog.Catalog, c Case) (map[string]any, router.Request) {
	st := make(map[string]any, len(c.State))
	for k, v := range c.State {
		if k != "mid_turn" {
			st[k] = v
		}
	}
	req := router.Request{Scope: c.Scope, Warm: c.Warm, MidTurn: c.State["mid_turn"] == true}
	if c.Scope != catalog.ScopeMain {
		return st, req
	}
	task, _ := c.State["task"].(string)
	cs, _ := c.State["current"].(map[string]any)
	curTier, _ := cs["tier"].(string)
	curMode, _ := cs["mode"].(string)
	model := c.sessionModel(cat)
	if c.State["phase"] != "initial" {
		// The work in progress: as labeled, else the decision in force
		// (the hooks' legacy sessions do the same); the paused work.
		if w, ok := c.State["work_in_progress"].(map[string]any); ok {
			req.Work, st["work_in_progress"] = labeledWork(cat, model, w, curMode)
		} else if cat.Tier(c.Scope, curTier) != nil {
			req.Work = &state.Work{Tier: curTier, Mode: curMode}
			st["work_in_progress"] = map[string]any{"level": router.WorkLevel(cat, curTier)}
		}
		if w, ok := c.State["paused_work"].(map[string]any); ok {
			var sent map[string]any
			req.Paused, sent = labeledWork(cat, model, w, "")
			delete(st, "paused_work")
			if kept, _ := w["kept"].(bool); kept && req.Paused != nil {
				req.Paused.Kept = true
			} else {
				st["paused_work"] = sent
			}
		}
	}
	req.Peer = transcript.IsPeer(task)
	req.GoAhead = !req.Peer && router.GoAhead(task)
	if !req.Peer {
		req.Explicit = router.ExplicitRequests(cat, model)
		if t := router.EffortTier(cat, model, "xhigh"); t != nil && router.Ultrathink(task) {
			req.MinTier = t.ID
		}
	}
	if c.Warm {
		req.Current = current(cat, c)
	}
	req.State = st // what Jev sees (router.Blind)
	return st, req
}

// current is the decision in force a main case describes (state.current):
// its tier, else the model a work asked for in words runs on, else the
// default tier.
func current(cat *catalog.Catalog, c Case) *state.Decision {
	cs, _ := c.State["current"].(map[string]any)
	tier, _ := cs["tier"].(string)
	mode, _ := cs["mode"].(string)
	effort, _ := cs["effort"].(string)
	model := c.sessionModel(cat)
	if t := cat.Tier(c.Scope, tier); t != nil {
		return &state.Decision{Scope: c.Scope, Tier: tier, Model: t.Model, Effort: t.Effort, Mode: mode}
	}
	if model != cat.DefaultTier(catalog.ScopeMain).Model {
		return &state.Decision{Scope: c.Scope, Tier: state.PinnedTier, Model: model, Effort: effort, Mode: mode}
	}
	t := cat.DefaultTier(c.Scope)
	return &state.Decision{Scope: c.Scope, Tier: t.ID, Model: t.Model, Effort: t.Effort}
}

// labeledWork reads a labeled work ({goal, level, mode, model, done};
// mode defaults to mode) and what the hooks would send Jev of it ({goal,
// level, done}).
func labeledWork(cat *catalog.Catalog, model string, w map[string]any, mode string) (*state.Work, map[string]any) {
	lv, _ := w["level"].(string)
	goal, _ := w["goal"].(string)
	wm, _ := w["model"].(string)
	done, _ := w["done"].(bool)
	if m, ok := w["mode"].(string); ok {
		mode = m
	}
	sent := map[string]any{"level": lv}
	if goal != "" {
		sent["goal"] = goal
	}
	if done {
		sent["done"] = true
	}
	t := levelTier(cat, model, lv)
	if t == nil {
		return nil, sent
	}
	return &state.Work{Tier: t.ID, Mode: mode, Model: modelKey(cat, wm), Goal: goal, Done: done}, sent
}

// levelTier is the main tier a work_in_progress level names: a tier ID or
// an effort on model.
func levelTier(cat *catalog.Catalog, model, level string) *catalog.Tier {
	if t := cat.Tier(catalog.ScopeMain, level); t != nil {
		return t
	}
	return router.EffortTier(cat, model, level)
}

func one(ctx context.Context, env *router.Env, c Case, format string) Result {
	cat, cl := env.Catalog, env.Jev
	r := Result{Case: c}
	st, req := setup(cat, c)
	fp := fastPath(env, c, req)
	ask := jev.Ask{Relation: req.Work != nil && fp == ""}
	ask.Resume = ask.Relation && router.Resumable(req.Paused)
	if fp == "" {
		_, back := proposalGoAhead(env, c, req)
		ask.Offer = ask.Relation && back
	}
	for _, x := range req.Explicit {
		ask.Explicit = append(ask.Explicit, x.Explicit)
	}
	qs, ids := jev.Questions(cat, c.Scope, ask)
	if format == "choice" {
		qs[jev.QLevel] = jev.TierQuestion(cat, c.Scope)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ans, resp, err := cl.Ask(ctx, cat.Meta.JevModel, "", st, qs)
	if resp != nil {
		r.Cost = resp.Usage.Cost
	}
	if err != nil {
		r.Err = err.Error()
		return r
	}
	lv := ans[jev.QLevel]
	if format == "choice" {
		r.Probs = lv.Probabilities
	} else {
		r.Probs = jev.LevelProbs(lv, ids)
	}
	r.Conf = lv.Confidence
	r.Got = argmax(r.Probs, ids)
	if a, ok := ans[jev.QRelation]; ok {
		r.RelP, r.RelConf = a.Probabilities, a.Confidence
	}
	if a, ok := ans[jev.QOffer]; ok && a.Noul != nil {
		p := *a.Noul
		r.OfferP = &p
	}
	if fp != "" {
		// The relation the hooks don't ask, alone in its own call (the
		// questions of the decision stay the hooks').
		q := map[string]jev.Question{jev.QRelation: jev.RelationQuestion(cat, router.Resumable(req.Paused))}
		if a, resp, err := cl.Ask(ctx, cat.Meta.JevModel, "", st, q); err == nil {
			r.RelP, r.RelConf = a[jev.QRelation].Probabilities, a[jev.QRelation].Confidence
			r.Cost += resp.Usage.Cost
		}
	}
	for id, a := range ans {
		switch {
		case a.Noul == nil:
		case strings.HasPrefix(id, jev.QModePfx):
			if r.ModeP == nil {
				r.ModeP = map[string]float64{}
			}
			r.ModeP[strings.TrimPrefix(id, jev.QModePfx)] = *a.Noul
		case strings.HasPrefix(id, jev.QTierPfx):
			if r.AskedP == nil {
				r.AskedP = map[string]float64{}
			}
			r.AskedP[strings.TrimPrefix(id, jev.QTierPfx)] = *a.Noul
		}
	}
	for id, p := range jev.ExplicitProbs(ans) {
		if r.ExplicitP == nil {
			r.ExplicitP = map[string]float64{}
		}
		r.ExplicitP[strings.TrimPrefix(id, jev.QExplicitPfx)] = p
	}
	if format != "choice" {
		r.judge(env, req, ans, ids)
	}
	return r
}

// judge applies the router's policy to Jev's answers, as the hooks would.
func (r *Result) judge(env *router.Env, req router.Request, ans map[string]jev.Answer, ids []string) {
	cat, c := env.Catalog, r.Case
	if req.Work != nil {
		r.WorkTier = req.Work.Tier
	}
	if req.Paused != nil {
		r.PausedTier = req.Paused.Tier
	}
	// A bare go-ahead carries the work in progress on without asking Jev
	// (the hooks' fast path on warm turns, and after a compaction or a
	// pause), unless it answers a proposal: then it is routed, not below
	// the work in progress.
	fp := fastPath(env, c, req)
	goAhead := fp == "go-ahead"
	if fp == router.FollowUpProposal {
		req.FollowUp = fp
	}
	if fp == "" {
		req.ProposalGoAhead, req.BackFirst = proposalGoAhead(env, c, req)
	}
	r.FastPath = fp
	var cur *catalog.Tier
	if req.Current != nil {
		cur = cat.Tier(c.Scope, req.Current.Tier)
	}
	// The router's verdict, with free switches (per-turn effort).
	v := env.Judge(req, env.Read(ans, ids, c.Scope), cur, policy.RepoPolicy{}, policy.Params{Penalty: cat.Meta.UnderprovisionPenalty, Scale: 1})
	r.Decision, r.Mode, r.Model, r.Kept, r.Hold = v.Tier.ID, v.Mode, v.Model, v.Keep, v.Hold
	if goAhead {
		// The hooks' own fast path (router.Carried).
		req.Current, req.Trigger = current(cat, c), map[string]string{"warm": "warm", "post_compact": "compact"}[fmt.Sprint(c.State["phase"])]
		if req.Trigger == "" {
			req.Trigger = "cold"
		}
		d, _ := env.Carried(req, policy.RepoPolicy{})
		r.Decision, r.Mode, r.Model, r.Kept, r.Hold = d.Tier, d.Mode, "", "go-ahead", ""
		if cat.Tier(c.Scope, d.Tier) == nil { // on a model outside the tiers: its level
			if t := router.EffortTier(cat, d.Model, d.Effort); t != nil {
				r.Decision = t.ID
			}
		}
		if d.Model != cat.DefaultTier(catalog.ScopeMain).Model {
			r.Model = d.Model
		}
	}
}

// fastPath says how the hooks take a case's prompt without the relation
// question: a bare go-ahead carried on without asking Jev ("go-ahead"), or
// a go-ahead to a proposal routed without it (router.FollowUpProposal),
// unless a detour paused work (the proposal may be to go back to it: the
// relation question is asked). Neither once a wrap-up closed the work,
// unless the paused work needs more (router.Acknowledges): then it is
// routed as any prompt.
func fastPath(env *router.Env, c Case, req router.Request) string {
	task, _ := c.State["task"].(string)
	last, _ := c.State["last_assistant"].(string)
	switch {
	case !env.Cfg.Features.FastPath || req.Work == nil || env.Acknowledges(req.Work, req.Paused) || req.Peer || !router.GoAhead(task):
		return ""
	case c.State["phase"] != "post_compact" && router.Proposes(last):
		if req.Paused != nil {
			return ""
		}
		return router.FollowUpProposal
	}
	return "go-ahead"
}

// proposalGoAhead mirrors the hooks for a bare go-ahead to what the
// assistant asked, routed with the relation question (fastPath is ""):
// after a detour (paused work), or once a wrap-up closed the work
// (router.Acknowledges; right after a compaction too, whose summary may
// end on a proposal). It doesn't go below the work it follows unless it
// is answered alone or goes back, and after a detour it goes back first
// when the paused work needs more and the prompt wasn't typed mid-turn
// (router.Request.ProposalGoAhead, BackFirst).
func proposalGoAhead(env *router.Env, c Case, req router.Request) (goAhead, backFirst bool) {
	task, _ := c.State["task"].(string)
	last, _ := c.State["last_assistant"].(string)
	compact := c.State["phase"] == "post_compact"
	ack := req.Work != nil && env.Acknowledges(req.Work, req.Paused)
	if !env.Cfg.Features.FastPath || req.Work == nil || (req.Paused == nil && !ack) || req.Peer || !router.GoAhead(task) {
		return false, false
	}
	if ack {
		return compact || router.Proposes(last), false
	}
	if compact || !router.Proposes(last) {
		return false, false
	}
	_, backFirst = env.GoAheadWork(nil, req.Work, req.Paused, req.MidTurn)
	return true, backFirst
}

// Rejudge applies the policy of env's catalog (thresholds, penalty, rules)
// to the answers of a saved run (`automodel eval --json`), without asking
// Jev again: decision rules can be compared on the same answers. Answers
// to questions the catalog words differently are not re-asked.
func Rejudge(env *router.Env, rs []Result) []Result {
	out := make([]Result, 0, len(rs))
	for _, r := range rs {
		if r.Err != "" {
			out = append(out, r)
			continue
		}
		_, req := setup(env.Catalog, r.Case)
		_, ids := jev.Questions(env.Catalog, r.Scope, jev.Ask{})
		ans := map[string]jev.Answer{jev.QLevel: {Type: "score", Probabilities: map[string]float64{}, Confidence: r.Conf}}
		for i, id := range ids {
			ans[jev.QLevel].Probabilities[fmt.Sprint(i)] = r.Probs[id]
		}
		if r.RelP != nil && fastPath(env, r.Case, req) == "" {
			ans[jev.QRelation] = jev.Answer{Type: "choice", Probabilities: r.RelP, Confidence: r.RelConf}
		}
		if r.OfferP != nil {
			p := *r.OfferP
			ans[jev.QOffer] = jev.Answer{Type: "noul", Noul: &p}
		}
		for _, n := range []struct {
			pfx string
			p   map[string]float64
		}{{jev.QModePfx, r.ModeP}, {jev.QTierPfx, r.AskedP}, {jev.QExplicitPfx, r.ExplicitP}} {
			for k, p := range n.p {
				ans[n.pfx+k] = jev.Answer{Type: "noul", Noul: &p}
			}
		}
		r.Decision, r.Mode, r.Model, r.Kept, r.Hold, r.WorkTier, r.PausedTier, r.FastPath = "", "", "", "", "", "", "", ""
		r.judge(env, req, ans, ids)
		out = append(out, r)
	}
	return out
}
func argmax(p map[string]float64, order []string) string {
	best, bp := "", -1.0
	for _, id := range order {
		if p[id] > bp {
			best, bp = id, p[id]
		}
	}
	return best
}

// Summary aggregates results.
type Summary struct {
	Cases, Errors                int
	Exact, Acceptable            float64
	DecisionOK                   float64 // router verdict within accept
	MeanConf, ConfRight, ConfBad float64
	Buckets                      []Bucket
	Modes                        map[string]*Binary
	// ModeDecision scores the router's mode (on or off) against the labels.
	ModeDecision map[string]*ModeCount
	// Relation scores the relation Choice, Explicit the requests confirmed
	// in words; Follow counts the decisions below the work in progress on
	// cases that hold it (a follow-up relation, mid-turn, a peer message);
	// Model scores the model the decision runs on where the case asks for
	// one, follows up work that runs on one, or the router moved it.
	Relation RelationStats
	Explicit ExplicitStats
	// Offer scores the offer question (a go-ahead after a detour whose
	// paused work needs more) against the relation labels: yes unless the
	// label is resume.
	Offer   *Binary
	Follow  Count
	Model   Count
	CostUSD float64
	// Scopes holds the per-scope tier metrics: exact accuracy, recall per
	// tier, confusion matrices, tier share against label share, rank
	// error and calibration.
	Scopes map[string]*ScopeStats
}

type Bucket struct {
	Lo, Hi   float64
	N        int
	Accuracy float64
}

// Count is N answers of which Right were right (Follow: Right counts the
// decisions not below the work in progress).
type Count struct{ N, Right int }

// ModeCount scores a mode's on/off decision: over all cases, and on the
// cases labeled on and off.
type ModeCount struct {
	Count
	On, Off Count
}

// Binary scores a yes/no question against its labels at a threshold.
type Binary struct {
	Threshold          float64
	N, Right           int
	MeanYesP, MeanNoP  float64
	MinYesP, MaxNoP    float64 // the gap a threshold can sit in
	nYes, nNo          int
	sumYesP, sumNoP    float64
	FalseYes, FalseNos int
}

func (b *Binary) add(p float64, want bool) {
	b.N++
	got := p >= b.Threshold
	if got == want {
		b.Right++
	} else if got {
		b.FalseYes++
	} else {
		b.FalseNos++
	}
	if want {
		if b.nYes == 0 || p < b.MinYesP {
			b.MinYesP = p
		}
		b.nYes++
		b.sumYesP += p
	} else {
		b.MaxNoP = max(b.MaxNoP, p)
		b.nNo++
		b.sumNoP += p
	}
}

func (b *Binary) finish() {
	if b.nYes > 0 {
		b.MeanYesP = b.sumYesP / float64(b.nYes)
	}
	if b.nNo > 0 {
		b.MeanNoP = b.sumNoP / float64(b.nNo)
	}
}

func Summarize(cat *catalog.Catalog, rs []Result) Summary {
	s := Summary{Modes: map[string]*Binary{}, ModeDecision: map[string]*ModeCount{}}
	bounds := []float64{0, 0.35, 0.6, 0.8, 1.01}
	for i := 0; i+1 < len(bounds); i++ {
		s.Buckets = append(s.Buckets, Bucket{Lo: bounds[i], Hi: bounds[i+1]})
	}
	bucketRight := make([]int, len(s.Buckets))
	var right, ok, nRight, nBad, dec, decN int
	for _, r := range rs {
		s.CostUSD += r.Cost
		if r.Err != "" {
			s.Errors++
			continue
		}
		s.Cases++
		s.MeanConf += r.Conf
		good := contains(r.acceptSet(), r.Got)
		if r.Decision != "" {
			decN++
			if contains(r.acceptSet(), r.Decision) {
				dec++
			}
		}
		if r.Got == r.Want {
			right++
		}
		if good {
			ok++
			s.ConfRight += r.Conf
			nRight++
		} else {
			s.ConfBad += r.Conf
			nBad++
		}
		for i := range s.Buckets {
			if r.Conf >= s.Buckets[i].Lo && r.Conf < s.Buckets[i].Hi {
				s.Buckets[i].N++
				if good {
					bucketRight[i]++
				}
			}
		}
		if r.OfferP != nil && r.Relation != "" {
			if s.Offer == nil {
				s.Offer = &Binary{Threshold: cat.Meta.DetourOfferThreshold()}
			}
			s.Offer.add(*r.OfferP, r.Relation != catalog.RelationResume)
		}
		for m, want := range r.Modes {
			if p, has := r.ModeP[m]; has {
				b := s.Modes[m]
				if b == nil {
					th := 0.5
					if md := cat.Modes[m]; md != nil {
						th = md.Threshold
					}
					b = &Binary{Threshold: th}
					s.Modes[m] = b
				}
				b.add(p, want)
			}
			if r.Decision != "" {
				c := s.ModeDecision[m]
				if c == nil {
					c = &ModeCount{}
					s.ModeDecision[m] = c
				}
				side := &c.Off
				if want {
					side = &c.On
				}
				c.N++
				side.N++
				if (r.Mode == m) == want {
					c.Right++
					side.Right++
				}
			}
		}
		// A follow-up below the work it holds (the paused work when it goes
		// back to it), unless the user asked for that effort.
		work := r.WorkTier
		if r.Relation == catalog.RelationResume && r.PausedTier != "" {
			work = r.PausedTier
		}
		if w, d := cat.Tier(r.Scope, work), cat.Tier(r.Scope, r.Decision); r.holds() && w != nil && d != nil && (r.Explicit == nil || r.Explicit.Effort == "") {
			s.Follow.N++
			if d.Rank >= w.Rank {
				s.Follow.Right++
			}
		}
		if want := r.wantModel(cat); r.Scope == catalog.ScopeMain && r.Decision != "" && (want != "" || r.Model != "") {
			s.Model.N++
			if r.Model == want {
				s.Model.Right++
			}
		}
	}
	s.Relation = RelationMetrics(rs)
	s.Explicit = ExplicitMetrics(cat, rs)
	if s.Cases > 0 {
		s.Exact = float64(right) / float64(s.Cases)
		s.Acceptable = float64(ok) / float64(s.Cases)
		s.MeanConf /= float64(s.Cases)
	}
	if decN > 0 {
		s.DecisionOK = float64(dec) / float64(decN)
	}
	if nRight > 0 {
		s.ConfRight /= float64(nRight)
	}
	if nBad > 0 {
		s.ConfBad /= float64(nBad)
	}
	for i := range s.Buckets {
		if s.Buckets[i].N > 0 {
			s.Buckets[i].Accuracy = float64(bucketRight[i]) / float64(s.Buckets[i].N)
		}
	}
	for _, b := range s.Modes {
		b.finish()
	}
	if s.Offer != nil {
		s.Offer.finish()
	}
	s.Scopes = map[string]*ScopeStats{}
	for _, sc := range []string{catalog.ScopeMain, catalog.ScopeSubagent} {
		if st := ScopeMetrics(cat, sc, rs); st.N > 0 {
			s.Scopes[sc] = st
		}
	}
	return s
}

// Print writes a per-case table and the summary.
func Print(w io.Writer, cat *catalog.Catalog, rs []Result, s Summary) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Scope < rs[j].Scope })
	fmt.Fprintf(w, "| case | want | Jev | p(want) | conf | decision | modes | relation |\n|---|---|---|---:|---:|---|---|---|\n")
	for _, r := range rs {
		if r.Err != "" {
			fmt.Fprintf(w, "| %s | %s | error: %s | | | | | |\n", r.ID, r.Want, r.Err)
			continue
		}
		mark := ""
		if !contains(r.acceptSet(), r.Got) {
			mark = " ✗"
		}
		var modes []string
		for m, p := range r.ModeP {
			want := ""
			if v, ok := r.Modes[m]; ok {
				want = map[bool]string{true: "(yes)", false: "(no)"}[v]
			}
			modes = append(modes, fmt.Sprintf("%s %.2f%s", m, p, want))
		}
		for t, p := range r.AskedP {
			modes = append(modes, fmt.Sprintf("tier %s %.2f", t, p))
		}
		for x, p := range r.ExplicitP {
			modes = append(modes, fmt.Sprintf("asks %s %.2f", x, p))
		}
		sort.Strings(modes)
		rel := ""
		if top, p := relationTop(r.RelP); top != "" {
			rel = fmt.Sprintf("%s %.2f", top, p)
			if r.Relation != "" && r.Relation != top {
				rel += " ✗ (" + r.Relation + ")"
			}
		}
		decision := r.Decision
		if r.Decision != "" && !contains(r.acceptSet(), r.Decision) {
			decision += " ✗"
		}
		if r.Mode != "" {
			decision += " +" + r.Mode
		}
		if r.Model != "" {
			decision += " on " + r.Model
		}
		if r.Kept != "" && r.Kept != "same tier" {
			decision += " (kept: " + r.Kept + ")"
		}
		if r.Hold != "" {
			decision += " (" + r.Hold + ")"
		}
		fmt.Fprintf(w, "| %s | %s | %s%s | %.2f | %.2f | %s | %s | %s |\n", r.ID, r.Want, r.Got, mark, r.Probs[r.Want], r.Conf, decision, strings.Join(modes, ", "), rel)
	}
	fmt.Fprintln(w)
	PrintSummary(w, s)
}

// PrintSummary writes the summary without the per-case table.
func PrintSummary(w io.Writer, s Summary) {
	fmt.Fprintf(w, "%d cases (%d errors): Jev's top tier exact %.0f%%, acceptable %.0f%%; router decision acceptable %.0f%%\n", s.Cases, s.Errors, 100*s.Exact, 100*s.Acceptable, 100*s.DecisionOK)
	fmt.Fprintf(w, "confidence: mean %.2f, when acceptable %.2f, when not %.2f\n", s.MeanConf, s.ConfRight, s.ConfBad)
	for _, b := range s.Buckets {
		if b.N > 0 {
			fmt.Fprintf(w, "  confidence [%.2f, %.2f): %d cases, %.0f%% acceptable\n", b.Lo, math.Min(b.Hi, 1), b.N, 100*b.Accuracy)
		}
	}
	for m, b := range s.Modes {
		fmt.Fprintf(w, "mode %s @%.2f: %d/%d right (false yes %d, false no %d), mean p yes-cases %.2f, no-cases %.2f\n",
			m, b.Threshold, b.Right, b.N, b.FalseYes, b.FalseNos, b.MeanYesP, b.MeanNoP)
	}
	for m, c := range s.ModeDecision {
		fmt.Fprintf(w, "mode %s on/off (router decision): %d/%d right (on %d/%d, off %d/%d)\n", m, c.Right, c.N, c.On.Right, c.On.N, c.Off.Right, c.Off.N)
	}
	if b := s.Offer; b != nil {
		fmt.Fprintf(w, "detour offer @%.2f: %d/%d right (false yes %d, false no %d), yes-cases p %.2f and up, no-cases at most %.2f\n",
			b.Threshold, b.Right, b.N, b.FalseYes, b.FalseNos, b.MinYesP, b.MaxNoP)
	}
	if f := s.Follow; f.N > 0 {
		fmt.Fprintf(w, "follow-ups (continue, extend, inform, side_question, resume, mid-turn, peer): %d/%d decisions below the work they hold\n", f.N-f.Right, f.N)
	}
	if m := s.Model; m.N > 0 {
		fmt.Fprintf(w, "model the work runs on (router decision): %d/%d right\n", m.Right, m.N)
	}
	PrintRelation(w, s.Relation)
	if e := s.Explicit; e.TP+e.FP+e.FN > 0 {
		fmt.Fprintf(w, "explicit requests: precision %.0f%%, recall %.0f%% (%d confirmed right, %d false, %d missed)\n",
			100*e.Precision(), 100*e.Recall(), e.TP, e.FP, e.FN)
		for _, k := range ExplicitKinds {
			if c := e.ByKind[k]; c != nil {
				fmt.Fprintf(w, "  %s @%.2f: precision %.0f%%, recall %.0f%% (%d right, %d false, %d missed)\n", k, c.Threshold, 100*c.Precision(), 100*c.Recall(), c.TP, c.FP, c.FN)
			}
		}
	}
	for _, sc := range []string{catalog.ScopeMain, catalog.ScopeSubagent} {
		if st := s.Scopes[sc]; st != nil {
			PrintScope(w, st)
		}
	}
	fmt.Fprintf(w, "\nJev cost: $%.4f\n", s.CostUSD)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
