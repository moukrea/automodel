package eval

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
)

// ScopeStats are the tier metrics of one scope. Accuracy alone hides a
// router that collapses onto a few tiers (e.g. only low and xhigh): the
// per-class recall, the confusion matrices and the share of each tier
// against the label share show it.
type ScopeStats struct {
	Scope string
	// Tiers in rank order.
	Tiers []string
	N     int
	// Exact: Jev's top level is the label; DecisionExact: the router's
	// decision is the label. Acceptable/DecisionOK: within accept.
	Exact, Acceptable, DecisionExact, DecisionOK float64
	// MAE is the mean absolute rank distance between the label and Jev's
	// top level (MAEDecision: the router's decision). Under/Over count
	// decisions below/above the label.
	MAE, MAEDecision   float64
	Under, Over        int
	MeanPWant, MeanTop float64
	// ECE is the expected calibration error of Jev's top probability
	// against exact correctness (10 equal-width bins).
	ECE float64
	// Class holds, per tier: label count, recall, and share of answers.
	Class map[string]*Class
	// Confusion[want][got] (Jev's top) and ConfusionDecision[want][decision].
	Confusion, ConfusionDecision map[string]map[string]int
	// Unstable is the share of cases whose top level changed between
	// repeated runs (only with --repeat > 1).
	Unstable float64
	Cases    int
}

// Class is one tier's row: how often it is the label, how often each
// answer lands on it, and the recall of labels of this tier.
type Class struct {
	Labels, Top, Decision   int
	Recall, RecallDecision  float64
	LabelShare, TopShare    float64
	DecisionShare           float64
	rightTop, rightDecision int
}

// ScopeMetrics computes the tier metrics of one scope.
func ScopeMetrics(cat *catalog.Catalog, scope string, rs []Result) *ScopeStats {
	s := &ScopeStats{Scope: scope, Class: map[string]*Class{},
		Confusion: map[string]map[string]int{}, ConfusionDecision: map[string]map[string]int{}}
	rank := map[string]int{}
	for _, t := range cat.TiersByRank(scope) {
		s.Tiers = append(s.Tiers, t.ID)
		rank[t.ID] = t.Rank
		s.Class[t.ID] = &Class{}
	}
	type bin struct {
		n, right int
		p        float64
	}
	bins := make([]bin, 10)
	var exact, ok, dExact, dOK, dN int
	var absErr, absErrD float64
	tops := map[string]map[string]bool{}
	for _, r := range rs {
		if r.Err != "" || r.Scope != scope || r.Got == "" {
			continue
		}
		cw := s.Class[r.Want]
		if cw == nil {
			continue // label outside this catalog's tiers
		}
		s.N++
		if tops[r.ID] == nil {
			tops[r.ID] = map[string]bool{}
		}
		tops[r.ID][r.Got] = true
		cw.Labels++
		if c := s.Class[r.Got]; c != nil {
			c.Top++
		}
		inc(s.Confusion, r.Want, r.Got)
		if r.Got == r.Want {
			exact++
			cw.rightTop++
		}
		if contains(r.acceptSet(), r.Got) {
			ok++
		}
		absErr += math.Abs(float64(rank[r.Got] - rank[r.Want]))
		top := r.Probs[r.Got]
		s.MeanTop += top
		s.MeanPWant += r.Probs[r.Want]
		b := min(int(top*10), 9)
		bins[b].n++
		bins[b].p += top
		if r.Got == r.Want {
			bins[b].right++
		}
		if r.Decision != "" {
			dN++
			if c := s.Class[r.Decision]; c != nil {
				c.Decision++
			}
			inc(s.ConfusionDecision, r.Want, r.Decision)
			if r.Decision == r.Want {
				dExact++
				cw.rightDecision++
			}
			if contains(r.acceptSet(), r.Decision) {
				dOK++
			}
			d := rank[r.Decision] - rank[r.Want]
			absErrD += math.Abs(float64(d))
			switch {
			case d < 0:
				s.Under++
			case d > 0:
				s.Over++
			}
		}
	}
	s.Cases = len(tops)
	if s.N == 0 {
		return s
	}
	n := float64(s.N)
	s.Exact, s.Acceptable = float64(exact)/n, float64(ok)/n
	s.MAE, s.MeanTop, s.MeanPWant = absErr/n, s.MeanTop/n, s.MeanPWant/n
	for _, b := range bins {
		if b.n > 0 {
			s.ECE += float64(b.n) / n * math.Abs(float64(b.right)/float64(b.n)-b.p/float64(b.n))
		}
	}
	if dN > 0 {
		s.DecisionExact, s.DecisionOK, s.MAEDecision = float64(dExact)/float64(dN), float64(dOK)/float64(dN), absErrD/float64(dN)
	}
	for _, c := range s.Class {
		c.LabelShare, c.TopShare = float64(c.Labels)/n, float64(c.Top)/n
		if dN > 0 {
			c.DecisionShare = float64(c.Decision) / float64(dN)
		}
		if c.Labels > 0 {
			c.Recall = float64(c.rightTop) / float64(c.Labels)
			c.RecallDecision = float64(c.rightDecision) / float64(c.Labels)
		}
	}
	unstable := 0
	for _, g := range tops {
		if len(g) > 1 {
			unstable++
		}
	}
	s.Unstable = float64(unstable) / float64(len(tops))
	return s
}

// MinRecall is the lowest decision recall over tiers that have labels
// (the regression gate against a router that skips a tier).
func (s *ScopeStats) MinRecall() (string, float64) {
	id, lo := "", 2.0
	for _, t := range s.Tiers {
		if c := s.Class[t]; c.Labels > 0 && c.RecallDecision < lo {
			id, lo = t, c.RecallDecision
		}
	}
	return id, lo
}

// MaxShareGap is the largest |decision share − label share| over tiers.
func (s *ScopeStats) MaxShareGap() (string, float64) {
	id, hi := "", 0.0
	for _, t := range s.Tiers {
		c := s.Class[t]
		if g := math.Abs(c.DecisionShare - c.LabelShare); g > hi {
			id, hi = t, g
		}
	}
	return id, hi
}

// Gate is the regression gate a catalog must pass on the held-out cases
// (the refresh skill runs `automodel eval --split test --check`).
type Gate struct {
	MinExact    float64 // router decision exact accuracy
	MinRecall   float64 // decision recall of every tier with at least MinLabels answers
	MinLabels   int
	MaxShareGap float64 // |decision share − label share| of any tier
	MaxMAE      float64 // router decision mean absolute rank error
}

// DefaultGate is set from the 2026-09 routing-quality work: the fixed
// router scores 91% exact, recall ≥ 82% on low..xhigh, share gaps ≤ 4 points
// and MAE 0.09 on the held-out split; the collapsed one scored 79%, medium
// recall 62%, a 9-point share gap and MAE 0.24; the fixed code with the
// old penalty (3.0) 85%, MAE 0.15, xhigh at 27% of decisions for 20% of labels.
var DefaultGate = Gate{MinExact: 0.88, MinRecall: 0.80, MinLabels: 20, MaxShareGap: 0.06, MaxMAE: 0.12}

// Check lists the gate's failures (none: the scope passes).
func (s *ScopeStats) Check(g Gate) []string {
	var out []string
	if s.DecisionExact < g.MinExact {
		out = append(out, fmt.Sprintf("%s: decision exact %.0f%% < %.0f%%", s.Scope, 100*s.DecisionExact, 100*g.MinExact))
	}
	if s.MAEDecision > g.MaxMAE {
		out = append(out, fmt.Sprintf("%s: decision rank error %.2f > %.2f", s.Scope, s.MAEDecision, g.MaxMAE))
	}
	for _, t := range s.Tiers {
		c := s.Class[t]
		if c.Labels >= g.MinLabels && c.RecallDecision < g.MinRecall {
			out = append(out, fmt.Sprintf("%s: %s recall %.0f%% < %.0f%%", s.Scope, t, 100*c.RecallDecision, 100*g.MinRecall))
		}
		if gap := c.DecisionShare - c.LabelShare; math.Abs(gap) > g.MaxShareGap {
			out = append(out, fmt.Sprintf("%s: %s gets %.0f%% of decisions for %.0f%% of labels", s.Scope, t, 100*c.DecisionShare, 100*c.LabelShare))
		}
	}
	return out
}

func inc(m map[string]map[string]int, a, b string) {
	if m[a] == nil {
		m[a] = map[string]int{}
	}
	m[a][b]++
}

func (r Result) acceptSet() []string {
	if len(r.Accept) == 0 {
		return []string{r.Want}
	}
	return r.Accept
}

// PrintScope writes one scope's metrics as markdown tables.
func PrintScope(w io.Writer, s *ScopeStats) {
	if s.N == 0 {
		return
	}
	fmt.Fprintf(w, "\n### %s: %d answers on %d cases\n\n", s.Scope, s.N, s.Cases)
	fmt.Fprintf(w, "| | Jev top | router decision |\n|---|---:|---:|\n")
	fmt.Fprintf(w, "| exact | %.0f%% | %.0f%% |\n| acceptable | %.0f%% | %.0f%% |\n", 100*s.Exact, 100*s.DecisionExact, 100*s.Acceptable, 100*s.DecisionOK)
	fmt.Fprintf(w, "| mean abs rank error | %.2f | %.2f |\n", s.MAE, s.MAEDecision)
	fmt.Fprintf(w, "\ndecisions below the label %d, above %d; mean p(label) %.2f, mean top p %.2f, ECE %.2f", s.Under, s.Over, s.MeanPWant, s.MeanTop, s.ECE)
	if s.Unstable > 0 {
		fmt.Fprintf(w, "; top changed across runs on %.0f%% of cases", 100*s.Unstable)
	}
	fmt.Fprintf(w, "\n\n| tier | labels | label share | top share | decision share | recall (top) | recall (decision) |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, t := range s.Tiers {
		c := s.Class[t]
		rec, recD := "–", "–"
		if c.Labels > 0 {
			rec, recD = fmt.Sprintf("%.0f%%", 100*c.Recall), fmt.Sprintf("%.0f%%", 100*c.RecallDecision)
		}
		fmt.Fprintf(w, "| %s | %d | %.0f%% | %.0f%% | %.0f%% | %s | %s |\n", t, c.Labels, 100*c.LabelShare, 100*c.TopShare, 100*c.DecisionShare, rec, recD)
	}
	for _, m := range []struct {
		name string
		c    map[string]map[string]int
	}{{"Jev top", s.Confusion}, {"router decision", s.ConfusionDecision}} {
		fmt.Fprintf(w, "\nconfusion, label (rows) × %s (columns):\n\n| label \\ got | %s |\n|---|%s\n", m.name, strings.Join(s.Tiers, " | "), strings.Repeat("---:|", len(s.Tiers)))
		for _, want := range s.Tiers {
			if s.Class[want].Labels == 0 {
				continue
			}
			row := make([]string, len(s.Tiers))
			for i, got := range s.Tiers {
				row[i] = fmt.Sprint(m.c[want][got])
			}
			fmt.Fprintf(w, "| %s | %s |\n", want, strings.Join(row, " | "))
		}
	}
}
