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
	// that makes them an effort ("en high", "switch to low", "high effort",
	// "set the effort to medium", "mets l'effort à low", "effort élevé").
	effortAnyRE  = words(`(x-?high|max)`)
	effortNearRE = words(`(?:(?:effort|niveau|reasoning|raisonnement)(?:\s*[:=]\s*|\s+(?:\S+\s+){0,2})|(?:en|in|at|mode|passe[rz]?(?:\s+(?:en|à|a))?|switch(?:\s+to)?|set(?:\s+it)?\s+to|use|utilise[rz]?)\s+(?:the\s+|le\s+|l'|du\s+)?)(low|medium|high|[ée]lev[ée]e?|haut|faible|bas|moyen)`)
	effortPostRE = words(`(low|medium|high)[\s-]+(?:reasoning[\s-]+)?(?:effort|reasoning)`)
	// moreRE finds the words that may ask for more thinking, for Jev to
	// confirm; narrowMoreRE the forms that ask for it whatever the context,
	// which count without Jev in metadata mode (it never sees the words).
	moreRE       = words(`think\s+(?:really\s+|very\s+|much\s+|a\s+lot\s+)?hard(?:er)?|think\s+(?:more|deeply|carefully|longer|it\s+through)|take\s+your\s+time|be\s+thorough|dig\s+deeper|r[ée]fl[ée]chi(?:s|ssez|sse|r)\s+(?:plus|bien|davantage|longtemps|en\s+profondeur|[àa]\s+fond)|prends?\s+(?:ton|le|bien\s+le)\s+temps|creuse\s+(?:plus|bien|davantage)|[àa]\s+fond|en\s+profondeur|met(?:s|z|tre)?\s+le\s+paquet`)
	narrowMoreRE = words(`think\s+(?:harder|more|deeply)|r[ée]fl[ée]chi(?:s|ssez)\s+(?:bien|plus|davantage|en\s+profondeur)|prends?\s+ton\s+temps`)
	// A mode word asks both questions, the mode and its refusal ("no need
	// for ultracode here", "n'utilise pas ultracode", "skip ultracode"): a
	// regex can't tell them apart.
	modeRE       = words(`ultracode|workflows?|en\s+parall[èe]le|plusieurs\s+agents|parallel\s+agents|multi-?agents?|in\s+parallel|(?:several|multiple|many)\s+(?:sub-?)?agents|sub-?agents\s+in\s+parallel`)
	modelRE      = words(`(opus|sonnet|haiku|fable)`)
	ultrathinkRE = words(`ultrathink`)
)

// effortName is the effort a word the regexes found names ("élevé": high).
func effortName(w string) string {
	switch w = strings.ReplaceAll(strings.ToLower(w), "é", "e"); {
	case strings.HasPrefix(w, "elev"), w == "haut":
		return "high"
	case w == "faible", w == "bas":
		return "low"
	case w == "moyen":
		return "medium"
	}
	return w
}

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
			efforts[effortName(m[1])] = true
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
			out = append(out, Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: md.ID, Label: md.ID}},
				Candidate{Explicit: jev.Explicit{Kind: jev.ExplicitMode, Value: jev.ExplicitOff, Label: md.ID}})
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
