package router

import (
	"regexp"
	"sort"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
)

// Explicit requests in prose ("passe en xhigh", "fais ça en ultracode",
// "think harder"): a regex finds the words that may make one, and Jev
// answers, for each, whether the prompt asks for it or only mentions it.
// In this kind of work effort names are often talked about ("why did it
// stay at xhigh?"), so a regex alone would misfire.

// Candidate is an explicit request found in a prompt, with the main tier an
// effort maps to on the session's model.
type Candidate struct {
	jev.Explicit
	Tier string
}

// words matches an alternation as whole words; Go's \b only knows ASCII
// letters, and French words start with accented ones.
func words(alt string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:` + alt + `)(?:[^\pL\pN_]|$)`)
}

var (
	// xhigh and max anywhere; low, medium and high only next to a word
	// that makes them an effort ("en high", "switch to low", "high effort").
	effortAnyRE  = words(`(x-?high|max)`)
	effortNearRE = words(`(?:effort(?:\s+level)?\s*[:=]?|en|in|at|mode|passe[rz]?(?:\s+(?:en|à|a))?|switch(?:\s+to)?|set(?:\s+it)?\s+to|use|utilise[rz]?)\s+(?:the\s+|le\s+|l'|du\s+)?(low|medium|high)`)
	effortPostRE = words(`(low|medium|high)[\s-]+effort`)
	moreRE       = words(`think\s+(?:really\s+|very\s+|much\s+|a\s+lot\s+)?hard(?:er)?|think\s+(?:more|deeply|carefully|longer|it\s+through)|take\s+your\s+time|be\s+thorough|dig\s+deeper|r[ée]fl[ée]chi(?:s|ssez|sse|r)\s+(?:plus|bien|davantage|longtemps|en\s+profondeur|[àa]\s+fond)|prends?\s+(?:ton|le|bien\s+le)\s+temps|creuse\s+(?:plus|bien|davantage)|[àa]\s+fond|en\s+profondeur|met(?:s|z|tre)?\s+le\s+paquet`)
	modeWords    = `ultracode|workflows?|en\s+parall[èe]le|agents\s+en\s+parall[èe]le|plusieurs\s+agents|parallel\s+agents|multi-?agents?`
	modeRE       = words(modeWords)
	modeOffRE    = words(`(?:pas\s+besoin\s+d[e']\s*|pas\s+d[e']\s*|plus\s+d[e']\s*|sans\s+|no\s+|without\s+|don'?t\s+use\s+|do\s+not\s+use\s+|stop(?:\s+using)?\s+|arr[êe]te(?:\s+(?:le|les|l'|d'utiliser))?\s*|d[ée]sactive(?:\s+(?:le|l'))?\s*|turn\s+off\s+|disable\s+)(?:the\s+|le\s+|l'|les\s+)?(?:mode\s+)?(?:` + modeWords + `)|(?:ultracode|workflows?)\s+off`)
	modelRE      = words(`(opus|sonnet|haiku|fable)`)
	ultrathinkRE = words(`ultrathink`)
)

// Ultrathink reports Claude Code's ultrathink keyword: a floor at xhigh,
// without asking Jev (it is a keyword, not prose).
func Ultrathink(prompt string) bool { return ultrathinkRE.MatchString(prompt) }

// ExplicitCandidates lists the requests the words of a prompt may make: an
// effort the session's model has a main tier for, more thinking, a mode
// that runs workflows (or its refusal), and a model a [model:X] tag could
// pin, other than the session's.
func ExplicitCandidates(c *catalog.Catalog, prompt, model string) []Candidate {
	var out []Candidate
	efforts := map[string]bool{}
	for _, m := range effortAnyRE.FindAllStringSubmatch(prompt, -1) {
		efforts[strings.ReplaceAll(strings.ToLower(m[1]), "-", "")] = true
	}
	for _, re := range []*regexp.Regexp{effortNearRE, effortPostRE} {
		for _, m := range re.FindAllStringSubmatch(prompt, -1) {
			efforts[strings.ToLower(m[1])] = true
		}
	}
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if t := EffortTier(c, model, e); efforts[e] && t != nil {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitEffort, Value: e, Label: e}, Tier: t.ID})
		}
	}
	if moreRE.MatchString(prompt) {
		out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitEffort, Value: jev.ExplicitMore}})
	}
	for _, md := range c.ModesFor(catalog.ScopeMain) {
		if !md.Workflows {
			continue
		}
		if modeRE.MatchString(prompt) || strings.Contains(strings.ToLower(prompt), md.ID) {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: md.ID, Label: md.ID}})
		}
		if modeOffRE.MatchString(prompt) {
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: jev.ExplicitOff, Label: md.ID}})
		}
		break // one workflow mode: the words are the same
	}
	var models []string
	seen := map[string]bool{}
	for _, m := range modelRE.FindAllStringSubmatch(prompt, -1) {
		if key := MainModel(c, strings.ToLower(m[1])); key != "" && key != model && !seen[key] {
			seen[key] = true
			models = append(models, key)
		}
	}
	sort.Strings(models)
	for _, key := range models {
		out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitModel, Value: key, Label: c.Model(key).Label}})
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
// metadata mode Jev never sees the words): more thinking only raises the
// floor, so a mention costs little there.
func (rd *Reading) unconfirmedMore(cs []Candidate) {
	for _, x := range cs {
		if x.Kind == jev.ExplicitEffort && x.Value == jev.ExplicitMore {
			if rd.explicit == nil {
				rd.explicit = map[string]float64{}
			}
			rd.explicit[x.ID()] = 1
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
// meta.explicit_model_threshold; of two efforts (or models) the more likely
// wins, and so does the more likely of a mode asked and refused.
func (e *Env) confirmed(req Request, rd Reading) asks {
	var a asks
	var effortP, modelP, onP, offP float64
	for _, x := range req.Explicit {
		p, ok := rd.explicit[x.ID()]
		if !ok || p < ExplicitThreshold(e.Catalog, x.Kind) {
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
