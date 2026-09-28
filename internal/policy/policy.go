// Package policy turns a Jev answer into a tier: confidence escalation
// (spec §6.3), per-repo floor/ceiling, and context-window constraints.
package policy

import (
	"math"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
)

type Thresholds struct{ Act, Low float64 }

// RepoPolicy is read from a file at the repository root (e.g. .automodel.toml).
type RepoPolicy struct {
	MinTier         string `toml:"min_tier"`
	MaxTier         string `toml:"max_tier"`
	MinSubagentTier string `toml:"min_subagent_tier"`
	MaxSubagentTier string `toml:"max_subagent_tier"`
	// Privacy overrides the global privacy mode for this repository
	// ("metadata": no text is sent to Jev).
	Privacy string `toml:"privacy"`
	// DisableModes turns modes off in this repository (e.g. ["ultracode"]).
	DisableModes []string `toml:"disable_modes"`
}

// ModeAllowed reports whether the repository allows a mode.
func (p RepoPolicy) ModeAllowed(id string) bool {
	for _, m := range p.DisableModes {
		if m == id {
			return false
		}
	}
	return true
}

// LoadRepoPolicy looks for name in dir and its parents, stopping at a .git
// directory or the filesystem root.
func LoadRepoPolicy(dir, name string) RepoPolicy {
	var p RepoPolicy
	if name == "" || dir == "" {
		return p
	}
	for d := dir; ; d = filepath.Dir(d) {
		f := filepath.Join(d, name)
		if _, err := os.Stat(f); err == nil {
			toml.DecodeFile(f, &p)
			return p
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			return p
		}
	}
}

func (p RepoPolicy) bounds(scope string) (min, max string) {
	if scope == catalog.ScopeSubagent {
		return p.MinSubagentTier, p.MaxSubagentTier
	}
	return p.MinTier, p.MaxTier
}

// Choose applies the confidence policy:
//
//	confidence >= act → Jev's choice
//	confidence <  act → max(choice, second most likely)
//	confidence <  low → one more rank up
func Choose(c *catalog.Catalog, scope string, a *jev.Answer, th Thresholds) *catalog.Tier {
	chosen := c.Tier(scope, a.Choice)
	if chosen == nil {
		return c.DefaultTier(scope)
	}
	if a.Confidence >= th.Act {
		return chosen
	}
	for _, opt := range a.Ranked() {
		if opt == a.Choice {
			continue
		}
		if t := c.Tier(scope, opt); t != nil && t.Rank > chosen.Rank {
			chosen = t
		}
		break
	}
	if a.Confidence < th.Low {
		if up := nextUp(c, scope, chosen, func(*catalog.Tier) bool { return true }); up != nil {
			chosen = up
		}
	}
	return chosen
}

// Constrain applies the repo floor/ceiling and drops tiers whose model window
// is smaller than the current context (escalating to the next tier that fits).
func Constrain(c *catalog.Catalog, scope string, t *catalog.Tier, rp RepoPolicy, contextTokens int) *catalog.Tier {
	min, max := rp.bounds(scope)
	if lo := c.Tier(scope, min); lo != nil && t.Rank < lo.Rank {
		t = lo
	}
	if hi := c.Tier(scope, max); hi != nil && t.Rank > hi.Rank {
		t = hi
	}
	fits := func(t *catalog.Tier) bool { return c.Fits(t, contextTokens) }
	if !fits(t) {
		if up := nextUp(c, scope, t, fits); up != nil {
			return up
		}
	}
	return t
}

func nextUp(c *catalog.Catalog, scope string, from *catalog.Tier, ok func(*catalog.Tier) bool) *catalog.Tier {
	for _, t := range c.TiersByRank(scope) {
		if t.Rank > from.Rank && ok(t) {
			return t
		}
	}
	return nil
}

// ---- cost-aware policy (features.cost_aware)

// Params weighs the expected cost of a tier against the cost of switching.
type Params struct {
	// Penalty: working below the right tier costs Penalty times the cost
	// gap, working above it costs the gap once.
	Penalty float64
	// Scale converts catalog cost units into dollars for the work ahead
	// (session-calibrated cost per prompt times the switch horizon). It is
	// only compared against switch costs, which are in dollars.
	Scale float64
}

// Costs returns each tier's relative cost per task. Tiers without a
// measurement or cost get a geometric interpolation between known
// neighbours (or ×1.6 per rank when none is known).
func Costs(c *catalog.Catalog, scope string) map[string]float64 {
	tiers := c.TiersByRank(scope)
	out := make(map[string]float64, len(tiers))
	for _, t := range tiers {
		out[t.ID] = c.TierCost(t)
	}
	for i, t := range tiers {
		if out[t.ID] > 0 {
			continue
		}
		lo, hi := -1, -1
		for j := i - 1; j >= 0; j-- {
			if out[tiers[j].ID] > 0 {
				lo = j
				break
			}
		}
		for j := i + 1; j < len(tiers); j++ {
			if c.TierCost(tiers[j]) > 0 {
				hi = j
				break
			}
		}
		switch {
		case lo >= 0 && hi >= 0:
			a, b := out[tiers[lo].ID], c.TierCost(tiers[hi])
			out[t.ID] = a * math.Pow(b/a, float64(i-lo)/float64(hi-lo))
		case lo >= 0:
			out[t.ID] = out[tiers[lo].ID] * math.Pow(1.6, float64(i-lo))
		case hi >= 0:
			out[t.ID] = c.TierCost(tiers[hi]) / math.Pow(1.6, float64(hi-i))
		default:
			out[t.ID] = math.Pow(1.6, float64(i))
		}
	}
	return out
}

// Loss is the cost of working at tier x when tier t was the right one.
func Loss(cost map[string]float64, x, t string, penalty float64) float64 {
	cx, ct := cost[x], cost[t]
	if cx >= ct {
		return cx - ct
	}
	return penalty * (ct - cx)
}

// ExpectedLoss is Σ p(t)·Loss(x, t), in catalog cost units.
func ExpectedLoss(cost map[string]float64, probs map[string]float64, x string, penalty float64) float64 {
	var sum, tot float64
	for t, p := range probs {
		if _, ok := cost[t]; !ok || p <= 0 {
			continue
		}
		sum += p * Loss(cost, x, t, penalty)
		tot += p
	}
	if tot == 0 {
		return 0
	}
	return sum / tot
}

// Pick is a cost-aware decision.
type Pick struct {
	Tier *catalog.Tier
	// Loss is each tier's expected loss (catalog cost units).
	Loss map[string]float64
	// Gain is the dollars saved against staying on current, after the
	// switch cost (0 when there is no current tier).
	Gain       float64
	SwitchCost float64
}

// Best picks the tier with the lowest Scale·ExpectedLoss + switch cost.
// current may be nil (nothing to stay on: every switch is free). Ties keep
// current, then prefer the higher tier.
func Best(c *catalog.Catalog, scope string, probs map[string]float64, current *catalog.Tier, switchCost func(*catalog.Tier) float64, p Params) Pick {
	cost := Costs(c, scope)
	pk := Pick{Loss: map[string]float64{}}
	total := func(t *catalog.Tier) float64 {
		l := ExpectedLoss(cost, probs, t.ID, p.Penalty)
		pk.Loss[t.ID] = l
		v := p.Scale * l
		if current != nil && t.ID != current.ID && switchCost != nil {
			v += switchCost(t)
		}
		return v
	}
	best, bestV := current, math.Inf(1)
	if current != nil {
		bestV = total(current)
	}
	curV := bestV
	for _, t := range c.TiersByRank(scope) {
		if current != nil && t.ID == current.ID {
			continue
		}
		if v := total(t); v < bestV-1e-12 || (best != nil && current == nil && v <= bestV+1e-12 && t.Rank > best.Rank) {
			best, bestV = t, v
		}
	}
	pk.Tier = best
	if current != nil && best != current {
		pk.Gain = curV - bestV
		if switchCost != nil {
			pk.SwitchCost = switchCost(best)
		}
	}
	return pk
}

// MaxGain is the most a switch away from current could save, whatever Jev
// answers: for each alternative, the best case (Jev certain of the tier that
// favours it most) minus its switch cost. At or below zero, asking Jev
// cannot change anything.
func MaxGain(c *catalog.Catalog, scope string, current *catalog.Tier, switchCost func(*catalog.Tier) float64, p Params) float64 {
	if current == nil {
		return math.Inf(1)
	}
	cost := Costs(c, scope)
	best := math.Inf(-1)
	for _, x := range c.TiersByRank(scope) {
		if x.ID == current.ID {
			continue
		}
		for _, t := range c.TiersByRank(scope) {
			g := p.Scale*(Loss(cost, current.ID, t.ID, p.Penalty)-Loss(cost, x.ID, t.ID, p.Penalty)) - switchCost(x)
			best = math.Max(best, g)
		}
	}
	return best
}
