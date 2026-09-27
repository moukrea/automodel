package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
)

// Savings compares what the routed requests cost with an estimate of the
// same work at one fixed effort (Claude Code's own setting, by default).
type Savings struct {
	Baseline    string  `json:"baseline"` // model·effort
	Requests    int     `json:"requests"`
	ActualUSD   float64 `json:"actual_usd"`
	BaselineUSD float64 `json:"baseline_usd"` // estimate
	SavedUSD    float64 `json:"saved_usd"`
	SavedPct    float64 `json:"saved_pct"`
	Unpriced    int     `json:"unpriced,omitempty"` // requests on a model without price or measurement
}

// EstimateSavings prices every routed request from its tokens and the
// catalog prices, and scales its output (response and thinking) to the
// baseline with the catalog's cost per task ratio (baseline tier / tier
// used); input and cache tokens are kept as they are. It's a conservative
// estimate: at a higher effort the same work also takes more turns, which
// it doesn't count.
func EstimateSavings(r io.Reader, since time.Time, c *catalog.Catalog, baseModel, baseEffort string) (*Savings, error) {
	base := costPerTask(c, baseModel, baseEffort)
	bm := c.Model(baseModel)
	if base <= 0 || bm == nil {
		return nil, fmt.Errorf("no cost measurement for %s at %s", baseModel, baseEffort)
	}
	s := &Savings{Baseline: bm.ShortLabel() + "·" + baseEffort}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var u Usage
		if json.Unmarshal(sc.Bytes(), &u) != nil || u.Kind != "usage" || !u.Routed || u.TS.Before(since) {
			continue
		}
		m := c.ModelByAPIID(u.Model)
		var key string
		if m != nil {
			key = modelKey(c, m)
		}
		cost := costPerTask(c, key, u.Effort)
		if m == nil || m.Price == nil || cost <= 0 {
			s.Unpriced++
			continue
		}
		p := m.Price
		write := p.CacheWrite1h
		if write == 0 {
			write = p.CacheWrite5m
		}
		in := (float64(u.InputTokens)*p.Input + float64(u.CacheReadInputTokens)*p.CacheRead +
			float64(u.CacheCreationInputTokens)*write) / 1e6
		out := float64(u.OutputTokens) * p.Output / 1e6
		s.Requests++
		s.ActualUSD += in + out
		// Effort changes how much the model writes and thinks, not what it
		// reads: only the output is scaled.
		s.BaselineUSD += in + out*base/cost
	}
	s.SavedUSD = s.BaselineUSD - s.ActualUSD
	if s.BaselineUSD > 0 {
		s.SavedPct = s.SavedUSD / s.BaselineUSD
	}
	return s, sc.Err()
}

// EstimateSavingsFile is EstimateSavings on a ledger file.
func EstimateSavingsFile(path string, since time.Time, c *catalog.Catalog, baseModel, baseEffort string) (*Savings, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return EstimateSavings(f, since, c, baseModel, baseEffort)
}

// costPerTask is the catalog's relative cost of model at effort: the
// benchmark measurement, else the cost of a tier running it.
func costPerTask(c *catalog.Catalog, model, effort string) float64 {
	if v := c.TierCost(&catalog.Tier{Model: model, Effort: effort}); v > 0 {
		return v
	}
	for _, scope := range []string{catalog.ScopeMain, catalog.ScopeSubagent} {
		for _, t := range c.TiersByRank(scope) {
			if t.Model == model && t.Effort == effort {
				return c.TierCost(t)
			}
		}
	}
	return 0
}

func modelKey(c *catalog.Catalog, m *catalog.Model) string {
	for k, v := range c.Models {
		if v == m {
			return k
		}
	}
	return ""
}

// Markdown renders the estimate.
func (s *Savings) Markdown(w io.Writer) {
	fmt.Fprintf(w, "\n## Estimated savings\n\n")
	fmt.Fprintf(w, "%d routed requests cost $%.2f. The same work at %s all along: about $%.2f (estimate).\n",
		s.Requests, s.ActualUSD, s.Baseline, s.BaselineUSD)
	fmt.Fprintf(w, "Saved: about $%.2f (%.0f%%).\n", s.SavedUSD, 100*s.SavedPct)
	if s.Unpriced > 0 {
		fmt.Fprintf(w, "(%d requests on a model without price or measurement are left out.)\n", s.Unpriced)
	}
	fmt.Fprintf(w, "\nHow: each request is priced from its tokens; only its output (response and thinking) is scaled by the catalog's cost-per-task ratio between the baseline and the tier used. Input and cache reads are counted as they were. Conservative: more turns at a higher effort aren't counted.\n")
}
