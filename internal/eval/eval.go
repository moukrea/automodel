// Package eval measures Jev's routing answers on labeled cases: accuracy,
// confidence and calibration for the tier level, the modes, the prompt's
// relation to the work in progress and the requests made in words. The
// refresh-model-catalog skill runs it after any change to the criteria,
// the questions or the Jev version.
package eval

import (
	"bufio"
	"cmp"
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
	// in progress is state.work_in_progress ({goal, level, mode, model}),
	// else the decision in state.current; state.paused_work the work a
	// detour paused; Jev sees their goal and level, as the hooks send them.
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
// mid-turn, a message from another session.
func (c Case) holds() bool {
	task, _ := c.State["task"].(string)
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
	AskedP    map[string]float64 `json:"asked_p,omitempty"`
	Cost      float64            `json:"cost_usd"`
	Err       string             `json:"error,omitempty"`
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
	curEffort, _ := cs["effort"].(string)
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
			req.Paused, st["paused_work"] = labeledWork(cat, model, w, "")
		}
	}
	req.Peer = transcript.IsPeer(task)
	if !req.Peer {
		req.Explicit = router.ExplicitCandidates(cat, task, model)
		if t := router.EffortTier(cat, model, "xhigh"); t != nil && router.Ultrathink(task) {
			req.MinTier = t.ID
		}
	}
	switch t := cat.Tier(c.Scope, curTier); {
	case !c.Warm:
	case t != nil:
		req.Current = &state.Decision{Tier: curTier, Model: t.Model, Effort: t.Effort, Mode: curMode}
	case model != cat.DefaultTier(catalog.ScopeMain).Model:
		req.Current = &state.Decision{Tier: state.PinnedTier, Model: model, Effort: curEffort, Mode: curMode}
	}
	return st, req
}

// labeledWork reads a labeled work ({goal, level, mode, model}; mode
// defaults to mode) and what the hooks would send Jev of it ({goal, level}).
func labeledWork(cat *catalog.Catalog, model string, w map[string]any, mode string) (*state.Work, map[string]any) {
	lv, _ := w["level"].(string)
	goal, _ := w["goal"].(string)
	wm, _ := w["model"].(string)
	if m, ok := w["mode"].(string); ok {
		mode = m
	}
	sent := map[string]any{"level": lv}
	if goal != "" {
		sent["goal"] = goal
	}
	t := levelTier(cat, model, lv)
	if t == nil {
		return nil, sent
	}
	return &state.Work{Tier: t.ID, Mode: mode, Model: modelKey(cat, wm), Goal: goal}, sent
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
	ask := jev.Ask{Relation: req.Work != nil}
	ask.Resume = ask.Relation && req.Paused != nil
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
		case strings.HasPrefix(id, jev.QExplicitPfx):
			if r.ExplicitP == nil {
				r.ExplicitP = map[string]float64{}
			}
			r.ExplicitP[strings.TrimPrefix(id, jev.QExplicitPfx)] = *a.Noul
		}
	}
	if format == "choice" {
		return r
	}
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
	task, _ := c.State["task"].(string)
	last, _ := c.State["last_assistant"].(string)
	goAhead := env.Cfg.Features.FastPath && req.Work != nil && !req.Peer && router.GoAhead(task)
	if goAhead && c.State["phase"] != "post_compact" && router.Proposes(last) {
		req.FollowUp, goAhead = "go-ahead to a proposal", false
	}
	var cur *catalog.Tier
	if req.Current != nil {
		cur = cat.Tier(c.Scope, req.Current.Tier)
	}
	// The router's verdict, with free switches (per-turn effort).
	v := env.Judge(req, env.Read(ans, ids, c.Scope), cur, policy.RepoPolicy{}, policy.Params{Penalty: cat.Meta.UnderprovisionPenalty, Scale: 1})
	r.Decision, r.Mode, r.Model, r.Kept, r.Hold = v.Tier.ID, v.Mode, v.Model, v.Keep, v.Hold
	if goAhead {
		w := *req.Work
		if req.MidTurn && cur != nil && cur.Rank > cat.Tier(c.Scope, w.Tier).Rank {
			w.Tier, w.Mode = cur.ID, cmp.Or(w.Mode, req.Current.Mode) // a go-ahead typed mid-turn lowers nothing
		}
		r.Decision, r.Mode, r.Model, r.Kept, r.Hold = w.Tier, w.Mode, w.Model, "go-ahead", ""
	}
	return r
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
	ModeDecision map[string]*Count
	// Relation scores the relation Choice, Explicit the requests confirmed
	// in words; Follow counts the decisions below the work in progress on
	// cases that hold it (a follow-up relation, mid-turn, a peer message);
	// Model scores the model the decision runs on where the case asks for
	// one, follows up work that runs on one, or the router moved it.
	Relation RelationStats
	Explicit ExplicitStats
	Follow   Count
	Model    Count
	CostUSD  float64
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

// Binary scores a yes/no question against its labels at a threshold.
type Binary struct {
	Threshold          float64
	N, Right           int
	MeanYesP, MeanNoP  float64
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
		b.nYes++
		b.sumYesP += p
	} else {
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
	s := Summary{Modes: map[string]*Binary{}, ModeDecision: map[string]*Count{}}
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
					c = &Count{}
					s.ModeDecision[m] = c
				}
				c.N++
				if (r.Mode == m) == want {
					c.Right++
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
		fmt.Fprintf(w, "mode %s on/off (router decision): %d/%d right\n", m, c.Right, c.N)
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
