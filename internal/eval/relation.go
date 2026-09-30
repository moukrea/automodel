package eval

import (
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
)

// FollowUp reports a relation label that follows the work in progress up
// (continue, extend, inform, side_question): its decision must not fall
// below the work's level.
func FollowUp(relation string) bool {
	for _, r := range catalog.Relations {
		if r == relation {
			return !catalog.Separate(relation)
		}
	}
	return false
}

// RelationStats scores the relation Choice on the cases that label it:
// how often Jev's top option is the label, the confusion matrix (label ×
// top option) and the expected calibration error of the top probability.
type RelationStats struct {
	N, Right  int
	Confusion map[string]map[string]int
	ECE       float64
}

// Accuracy is the share of answers whose top option is the label.
func (s RelationStats) Accuracy() float64 {
	if s.N == 0 {
		return 0
	}
	return float64(s.Right) / float64(s.N)
}

// RelationMetrics computes the relation metrics (10 equal-width bins for
// the ECE, as for the tier level).
func RelationMetrics(rs []Result) RelationStats {
	s := RelationStats{Confusion: map[string]map[string]int{}}
	type bin struct {
		n, right int
		p        float64
	}
	bins := make([]bin, 10)
	for _, r := range rs {
		top, p := relationTop(r.RelP)
		if r.Err != "" || r.Relation == "" || top == "" {
			continue
		}
		s.N++
		inc(s.Confusion, r.Relation, top)
		b := min(int(p*10), 9)
		bins[b].n++
		bins[b].p += p
		if top == r.Relation {
			s.Right++
			bins[b].right++
		}
	}
	for _, b := range bins {
		if b.n > 0 {
			s.ECE += float64(b.n) / float64(s.N) * math.Abs(float64(b.right)/float64(b.n)-b.p/float64(b.n))
		}
	}
	return s
}

// relationTop is the most likely relation and its probability ("" if none).
func relationTop(p map[string]float64) (string, float64) {
	top, bp := "", 0.0
	for _, r := range catalog.Relations {
		if p[r] > bp {
			top, bp = r, p[r]
		}
	}
	return top, bp
}

// PrintRelation writes the relation accuracy and its confusion matrix.
func PrintRelation(w io.Writer, s RelationStats) {
	if s.N == 0 {
		return
	}
	fmt.Fprintf(w, "relation: %d/%d right (%.0f%%), ECE of the top probability %.2f\n\n", s.Right, s.N, 100*s.Accuracy(), s.ECE)
	fmt.Fprintf(w, "| label \\ top | %s |\n|---|%s\n", strings.Join(catalog.Relations, " | "), strings.Repeat("---:|", len(catalog.Relations)))
	for _, want := range catalog.Relations {
		if len(s.Confusion[want]) == 0 {
			continue
		}
		row := make([]string, len(catalog.Relations))
		for i, got := range catalog.Relations {
			row[i] = fmt.Sprint(s.Confusion[want][got])
		}
		fmt.Fprintf(w, "| %s | %s |\n", want, strings.Join(row, " | "))
	}
	fmt.Fprintln(w)
}

// ExplicitStats scores the requests confirmed in words (a yes at
// meta.explicit_threshold) against the labels: a case without an explicit
// label asks for nothing, so a request confirmed there is a false one; a
// labeled request the regex missed, or Jev didn't confirm, is missed.
type ExplicitStats struct{ TP, FP, FN int }

func (e ExplicitStats) Precision() float64 {
	if e.TP+e.FP == 0 {
		return 0
	}
	return float64(e.TP) / float64(e.TP+e.FP)
}

func (e ExplicitStats) Recall() float64 {
	if e.TP+e.FN == 0 {
		return 0
	}
	return float64(e.TP) / float64(e.TP+e.FN)
}

// ExplicitMetrics computes precision and recall of the explicit requests
// over the main-scope answers.
func ExplicitMetrics(cat *catalog.Catalog, rs []Result) ExplicitStats {
	var e ExplicitStats
	th := cat.Meta.ExplicitThreshold()
	for _, r := range rs {
		if r.Err != "" || r.Scope != catalog.ScopeMain {
			continue
		}
		want := map[string]bool{}
		for _, id := range r.Explicit.requests(cat) {
			want[id] = true
		}
		for id, p := range r.ExplicitP {
			switch {
			case p < th:
			case want[id]:
				e.TP++
				delete(want, id)
			default:
				e.FP++
			}
		}
		e.FN += len(want)
	}
	return e
}
