package router

import (
	"regexp"
	"sort"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
)

// Explicit requests in prose ("passe en xhigh", "fais ça en ultracode",
// "think harder", "dial it back to medium", any language): Jev answers, for
// every request a prompt could make, whether it asks for it or only
// mentions it. No word list picks the questions: effort names are talked
// about far more often than asked for ("why did it stay at xhigh?"), and
// requests take any wording, so only Jev reads them.

// Candidate is an explicit request a prompt may make, with the main tier an
// effort maps to on the session's model.
type Candidate struct {
	jev.Explicit
	Tier string
}

var (
	// narrowMoreRE is the forms that ask for more thinking whatever the
	// context: in metadata mode Jev never sees the words, so only these
	// count, without Jev.
	narrowMoreRE = regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:think\s+(?:harder|more|deeply)|r[ée]fl[ée]chi(?:s|ssez)\s+(?:bien|plus|davantage|en\s+profondeur)|prends?\s+ton\s+temps)(?:[^\pL\pN_]|$)`)
	ultrathinkRE = regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])ultrathink(?:[^\pL\pN_]|$)`)
)

// Ultrathink reports Claude Code's ultrathink keyword: a floor at xhigh,
// without asking Jev (it is a keyword, not prose).
func Ultrathink(prompt string) bool { return ultrathinkRE.MatchString(prompt) }

// ExplicitRequests lists every request a prompt could make on a session
// running model, each one a question for Jev: an effort the session's model
// has a main tier for, more thinking, a mode that runs workflows and its
// refusal, and every other model that can run the main session under a
// name the user would give it (an alias: opus, sonnet, fable).
func ExplicitRequests(c *catalog.Catalog, model string) []Candidate {
	var out []Candidate
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if t := EffortTier(c, model, e); t != nil {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitEffort, Value: e, Label: e}, Tier: t.ID})
		}
	}
	out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitEffort, Value: jev.ExplicitMore}})
	for _, md := range c.ModesFor(catalog.ScopeMain) {
		if md.Workflows {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: md.ID, Label: md.ID}},
				Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: jev.ExplicitOff, Label: md.ID}})
			break // one workflow mode: the same request
		}
	}
	for _, key := range sortedModelKeys(c) {
		if md := c.Models[key]; md.Alias != "" && key != model && MainModel(c, key) == key {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitModel, Value: key, Label: md.Label}})
		}
	}
	return out
}

// EffortTier is the main tier that runs effort on model; on a model
// without efforts (Haiku) or without main tiers (one a work runs on, asked
// for in words), the default tier's model at that effort.
func EffortTier(c *catalog.Catalog, model, effort string) *catalog.Tier {
	if t := c.TierFor(catalog.ScopeMain, model, effort); t != nil {
		return t
	}
	return c.TierFor(catalog.ScopeMain, c.DefaultTier(catalog.ScopeMain).Model, effort)
}

// MainModel returns the catalog model a name designates (alias, catalog
// key or API ID) if it can run a main session (an API ID and at least the
// main window), else "".
func MainModel(c *catalog.Catalog, name string) string {
	for _, key := range sortedModelKeys(c) {
		md := c.Models[key]
		if (key == name || md.Alias == name || md.APIID == name) && md.APIID != "" && md.Context >= c.Meta.MinMainContext() {
			return key
		}
	}
	return ""
}

func sortedModelKeys(c *catalog.Catalog) []string {
	keys := make([]string, 0, len(c.Models))
	for k := range c.Models {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// unconfirmedMore counts a "think harder" request as asked without Jev (in
// metadata mode Jev never sees the words), only in the forms that ask for
// it whatever the context ("think harder", "réfléchis bien"): "le CPU
// tourne à fond" is no request. It raises this turn only, never the work
// in progress (Judge).
func (rd *Reading) unconfirmedMore(prompt string, cs []Candidate) {
	if !narrowMoreRE.MatchString(prompt) {
		return
	}
	for _, x := range cs {
		if x.Kind == jev.ExplicitEffort && x.Value == jev.ExplicitMore {
			if rd.explicit == nil {
				rd.explicit = map[string]float64{}
			}
			rd.explicit[x.ID()] = 1
			rd.guessedMore = true
		}
	}
}

// ExplicitThreshold is the yes-probability from which a request of kind
// counts.
func ExplicitThreshold(c *catalog.Catalog, kind string) float64 {
	if kind == jev.ExplicitModel {
		return c.Meta.ExplicitModelThreshold()
	}
	return c.Meta.ExplicitThreshold()
}

// LowerEffortP is the least yes-probability an effort below the work in
// progress needs: lowering hard work on a mention Jev misread ("set it to
// low for the subagents") costs far more than a missed request.
const LowerEffortP = 0.9

// RequestThreshold is the yes-probability from which request x counts,
// work being the tier of the work in progress (nil: none).
func RequestThreshold(c *catalog.Catalog, x Candidate, work *catalog.Tier) float64 {
	th := ExplicitThreshold(c, x.Kind)
	if t := c.Tier(catalog.ScopeMain, x.Tier); x.Kind == jev.ExplicitEffort && t != nil && work != nil && t.Rank < work.Rank {
		th = max(th, LowerEffortP)
	}
	return th
}

// asks is what a prompt explicitly asked for, as Jev confirmed it.
type asks struct {
	effort *catalog.Tier // an effort: the tier running it
	more   bool          // more thinking
	on     string        // a mode
	off    bool          // no mode
	model  string        // another model (catalog key)
}

func (a asks) any() bool { return a.effort != nil || a.more || a.on != "" || a.off || a.model != "" }

// confirmed reads Jev's answers to the explicit-request questions: a request
// counts from meta.explicit_threshold, a model from the stricter
// meta.explicit_model_threshold, an effort below the work in progress from
// LowerEffortP; of two efforts (or models) the more likely wins, and so
// does the more likely of a mode asked and refused.
func (e *Env) confirmed(req Request, rd Reading) asks {
	var a asks
	var effortP, modelP, onP, offP float64
	var work *catalog.Tier
	if req.Work != nil {
		work = e.Catalog.Tier(req.Scope, req.Work.Tier)
	}
	for _, x := range req.Explicit {
		p, ok := rd.explicit[x.ID()]
		if !ok || p < RequestThreshold(e.Catalog, x, work) {
			continue
		}
		switch {
		case x.Kind == jev.ExplicitEffort && x.Value == jev.ExplicitMore:
			a.more = true
		case x.Kind == jev.ExplicitEffort && p > effortP:
			if t := e.Catalog.Tier(catalog.ScopeMain, x.Tier); t != nil {
				a.effort, effortP = t, p
			}
		case x.Kind == jev.ExplicitMode && x.Value == jev.ExplicitOff:
			offP = p
		case x.Kind == jev.ExplicitMode && p > onP:
			a.on, onP = x.Value, p
		case x.Kind == jev.ExplicitModel && p > modelP:
			a.model, modelP = x.Value, p
		}
	}
	if offP > 0 && offP >= onP {
		a.on, a.off = "", true
	}
	return a
}
