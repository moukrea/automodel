// Package eval measures Jev's routing answers on labeled cases: accuracy,
// confidence and calibration for the tier level, the modes and the
// continuation question. The refresh-model-catalog skill runs it after any
// change to the criteria, the questions or the Jev version.
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
)

type Case struct {
	ID        string          `json:"id"`
	Scope     string          `json:"scope"`
	Warm      bool            `json:"warm"`
	State     map[string]any  `json:"state"`
	Want      string          `json:"want"`
	Accept    []string        `json:"accept"`
	Modes     map[string]bool `json:"modes"`
	Continues *bool           `json:"continues"`
	Note      string          `json:"note,omitempty"`
}

type Result struct {
	Case
	Got string `json:"got"`
	// Decision is what the router does with the answer (policy, gates,
	// modes); Kept says why a warm case stays on its current tier.
	Decision string             `json:"decision"`
	Mode     string             `json:"mode,omitempty"`
	Kept     string             `json:"kept,omitempty"`
	Probs    map[string]float64 `json:"probs"`
	Conf     float64            `json:"confidence"`
	ModeP    map[string]float64 `json:"mode_p,omitempty"`
	ContP    *float64           `json:"continues_p,omitempty"`
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

// Run asks Jev every case. format is "score" (the router's questions) or
// "choice" (the v1 tier Choice, for comparison).
func Run(ctx context.Context, env *router.Env, cases []Case, format string, parallel int) []Result {
	out := make([]Result, len(cases))
	sem := make(chan struct{}, max(1, parallel))
	var wg sync.WaitGroup
	for i, c := range cases {
		wg.Add(1)
		go func(i int, c Case) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = one(ctx, env, c, format)
		}(i, c)
	}
	wg.Wait()
	return out
}

func one(ctx context.Context, env *router.Env, c Case, format string) Result {
	cat, cl := env.Catalog, env.Jev
	r := Result{Case: c}
	qs, ids := jev.Questions(cat, c.Scope, c.Warm)
	if format == "choice" {
		qs[jev.QLevel] = jev.TierQuestion(cat, c.Scope)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ans, resp, err := cl.Ask(ctx, cat.Meta.JevModel, "", c.State, qs)
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
	for id, a := range ans {
		if m, ok := strings.CutPrefix(id, jev.QModePfx); ok && a.Noul != nil {
			if r.ModeP == nil {
				r.ModeP = map[string]float64{}
			}
			r.ModeP[m] = *a.Noul
		}
	}
	if a, ok := ans[jev.QContinues]; ok && a.Noul != nil {
		v := *a.Noul
		r.ContP = &v
	}
	if format != "choice" {
		// The router's verdict, with free switches (per-turn effort).
		req := router.Request{Scope: c.Scope, Warm: c.Warm}
		var cur *catalog.Tier
		if cs, ok := c.State["current"].(map[string]any); ok && c.Warm {
			id, _ := cs["tier"].(string)
			mode, _ := cs["mode"].(string)
			if cur = cat.Tier(c.Scope, id); cur != nil {
				req.Current = &state.Decision{Tier: id, Mode: mode}
			}
		}
		v := env.Judge(req, env.Read(ans, ids, c.Scope), cur, policy.RepoPolicy{}, policy.Params{Penalty: cat.Meta.UnderprovisionPenalty, Scale: 1})
		r.Decision, r.Mode, r.Kept = v.Tier.ID, v.Mode, v.Keep
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
	Continues                    Binary
	CostUSD                      float64
}

type Bucket struct {
	Lo, Hi   float64
	N        int
	Accuracy float64
}

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
	s := Summary{Modes: map[string]*Binary{}, Continues: Binary{Threshold: cat.Meta.ContinuesThreshold()}}
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
		good := contains(r.Accept, r.Got)
		if r.Decision != "" {
			decN++
			if contains(r.Accept, r.Decision) {
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
			p, has := r.ModeP[m]
			if !has {
				continue
			}
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
		if r.Continues != nil && r.ContP != nil {
			s.Continues.add(*r.ContP, *r.Continues)
		}
	}
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
	s.Continues.finish()
	return s
}

// Print writes a per-case table and the summary.
func Print(w io.Writer, cat *catalog.Catalog, rs []Result, s Summary) {
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].Scope < rs[j].Scope })
	fmt.Fprintf(w, "| case | want | Jev | p(want) | conf | decision | modes | continues |\n|---|---|---|---:|---:|---|---|---|\n")
	for _, r := range rs {
		if r.Err != "" {
			fmt.Fprintf(w, "| %s | %s | error: %s | | | | | |\n", r.ID, r.Want, r.Err)
			continue
		}
		mark := ""
		if !contains(r.Accept, r.Got) {
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
		cont := ""
		if r.ContP != nil {
			cont = fmt.Sprintf("%.2f", *r.ContP)
			if r.Continues != nil {
				cont += map[bool]string{true: " (yes)", false: " (no)"}[*r.Continues]
			}
		}
		decision := r.Decision
		if r.Decision != "" && !contains(r.Accept, r.Decision) {
			decision += " ✗"
		}
		if r.Mode != "" {
			decision += " +" + r.Mode
		}
		if r.Kept != "" && r.Kept != "same tier" {
			decision += " (kept: " + r.Kept + ")"
		}
		fmt.Fprintf(w, "| %s | %s | %s%s | %.2f | %.2f | %s | %s | %s |\n", r.ID, r.Want, r.Got, mark, r.Probs[r.Want], r.Conf, decision, strings.Join(modes, ", "), cont)
	}
	fmt.Fprintf(w, "\n%d cases (%d errors): Jev's top tier exact %.0f%%, acceptable %.0f%%; router decision acceptable %.0f%%\n", s.Cases, s.Errors, 100*s.Exact, 100*s.Acceptable, 100*s.DecisionOK)
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
	if b := s.Continues; b.N > 0 {
		fmt.Fprintf(w, "continues @%.2f: %d/%d right (false yes %d, false no %d), mean p yes-cases %.2f, no-cases %.2f\n",
			b.Threshold, b.Right, b.N, b.FalseYes, b.FalseNos, b.MeanYesP, b.MeanNoP)
	}
	fmt.Fprintf(w, "Jev cost: $%.4f\n", s.CostUSD)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
