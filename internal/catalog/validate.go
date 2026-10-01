package catalog

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultStaleDays is the age after which measurements and last_refresh warn.
const DefaultStaleDays = 60

// Validate applies the catalog rules (spec §2.3). The same rules live in the
// refresh skill's frontier.py; testdata/catalogs keeps both in agreement.
// effortOrder lists effort levels from least to most.
var effortOrder = []string{"low", "medium", "high", "xhigh", "max"}

// EffortRank returns an effort's position in effortOrder (-1 if unknown).
func EffortRank(e string) int {
	for i, x := range effortOrder {
		if x == e {
			return i
		}
	}
	return -1
}

func (c *Catalog) Validate(now time.Time, staleDays int) Issues {
	if staleDays <= 0 {
		staleDays = DefaultStaleDays
	}
	var is Issues
	errf := func(f string, a ...any) { is = append(is, Issue{"error", fmt.Sprintf(f, a...)}) }
	warnf := func(f string, a ...any) { is = append(is, Issue{"warning", fmt.Sprintf(f, a...)}) }

	m := c.Meta
	if m.Schema == 0 {
		errf("meta.schema is required")
	}
	for name, v := range map[string]string{
		"last_refresh": m.LastRefresh, "benchmark": m.Benchmark, "benchmark_version": m.BenchmarkVersion,
		"jev_model": m.JevModel, "default_main_tier": m.DefaultMainTier, "default_subagent_tier": m.DefaultSubagentTier,
	} {
		if v == "" {
			errf("meta.%s is required", name)
		}
	}
	if m.DefaultMainTier != "" && c.Tier(ScopeMain, m.DefaultMainTier) == nil {
		errf("meta.default_main_tier %q is not a main tier", m.DefaultMainTier)
	}
	if m.DefaultSubagentTier != "" && c.Tier(ScopeSubagent, m.DefaultSubagentTier) == nil {
		errf("meta.default_subagent_tier %q is not a subagent tier", m.DefaultSubagentTier)
	}

	for _, id := range sortedKeys(c.Models) {
		md := c.Models[id]
		if !contains(Statuses, md.Status) {
			errf("model %s: status %q is not one of %v", id, md.Status, Statuses)
		}
		if md.Status != "active" && md.Status != "" && md.Reason == "" {
			warnf("model %s: status %q without a reason", id, md.Status)
		}
		if md.Status == "active" {
			if md.APIID == "" {
				errf("model %s: active model needs api_id", id)
			}
			if md.Context <= 0 {
				errf("model %s: active model needs context", id)
			}
			switch {
			case md.Context > StandardContext && md.LongContext != "native" && md.LongContext != "beta":
				errf("model %s: context %d above %d needs long_context = \"native\" or \"beta\"", id, md.Context, StandardContext)
			case md.LongContext == "beta" && m.LongContextBeta == "":
				errf("model %s: long_context = \"beta\" needs meta.long_context_beta", id)
			}
			if md.PerTurnEffort && m.PerTurnEffortBeta == "" {
				errf("model %s: per_turn_effort needs meta.per_turn_effort_beta", id)
			}
			if md.Price == nil {
				errf("model %s: active model needs price", id)
			}
		}
		for _, s := range md.Scopes {
			if !contains(Scopes, s) {
				errf("model %s: unknown scope %q", id, s)
			}
		}
		if md.DefaultEffort != "" && !md.SupportsEffort(md.DefaultEffort) {
			errf("model %s: default_effort %q not in efforts", id, md.DefaultEffort)
		}
	}

	for _, scope := range sortedKeys(c.Tiers) {
		if !contains(Scopes, scope) {
			errf("tiers.%s: unknown scope (want %v)", scope, Scopes)
			continue
		}
		ranks := map[int]string{}
		for _, id := range sortedKeys(c.Tiers[scope]) {
			t := c.Tiers[scope][id]
			where := fmt.Sprintf("tier %s.%s", scope, id)
			if prev, dup := ranks[t.Rank]; dup {
				errf("%s: rank %d already used by %s", where, t.Rank, prev)
			}
			ranks[t.Rank] = id
			if t.Criteria == "" {
				errf("%s: criteria is empty", where)
			}
			md := c.Models[t.Model]
			switch {
			case md == nil:
				errf("%s: unknown model %q", where, t.Model)
				continue
			case md.Status != "active":
				errf("%s: model %s is %s, not active", where, t.Model, md.Status)
				continue
			case !md.AllowedIn(scope):
				errf("%s: model %s is restricted to scopes %v", where, t.Model, md.Scopes)
			}
			if t.Effort != "" && !md.SupportsEffort(t.Effort) {
				errf("%s: effort %q not supported by %s (%v)", where, t.Effort, t.Model, md.Efforts)
			}
			if t.Effort == "" && len(md.Efforts) > 0 {
				warnf("%s: no effort on %s, which supports %v", where, t.Model, md.Efforts)
			}
			if scope == ScopeMain && md.Context < m.MinMainContext() && (t.MaxContext <= 0 || t.MaxContext >= md.Context) {
				errf("%s: model %s has a %d-token window; main tiers need at least %d (meta.main_min_context), or a max_context below the window", where, t.Model, md.Context, m.MinMainContext())
			}
			if t.Asked() && (t.No == "" || t.Threshold <= 0 || t.Threshold >= 1) {
				errf("%s: an asked tier needs no and a threshold between 0 and 1", where)
			}
			if t.Asked() && (scope == ScopeMain && id == m.DefaultMainTier || scope == ScopeSubagent && id == m.DefaultSubagentTier) {
				errf("%s: the default tier can't be an asked tier", where)
			}
			if t.MaxContext > 0 && (scope == ScopeMain && id == m.DefaultMainTier || scope == ScopeSubagent && id == m.DefaultSubagentTier) {
				errf("%s: the default tier can't have a max_context", where)
			}
			if scope == ScopeSubagent && md.Alias == "" {
				errf("%s: model %s needs an alias (the Agent tool only accepts aliases)", where, t.Model)
			}
		}
	}

	if q := c.Questions.Relation; q != nil {
		if q.Question == "" {
			errf("questions.relation: question is required")
		}
		for _, id := range sortedKeys(q.Options) {
			switch o := q.Options[id]; {
			case !contains(Relations, id):
				errf("questions.relation: unknown option %s (want %v)", id, Relations)
			case o == nil || o.What == "":
				errf("questions.relation.options.%s: what is required", id)
			}
		}
		for _, id := range Relations {
			if _, ok := q.Options[id]; !ok {
				errf("questions.relation: option %s is missing", id)
			}
		}
	}
	if o := c.Questions.Offer; o != nil && (o.Question == "" || o.Yes == "" || o.No == "") {
		errf("questions.offer: question, yes and no are required")
	}
	if c.Questions.Explicit != nil {
		warnf("questions.explicit: deprecated, ignored (questions.explicit_effort, explicit_mode and explicit_model replaced it)")
	}
	for _, q := range []struct {
		name string
		r    *Relation
		ids  []string
	}{
		{"explicit_effort", c.Questions.ExplicitEffort, []string{"none", "low", "medium", "high", "xhigh", "max", "more"}},
		{"explicit_mode", c.Questions.ExplicitMode, []string{"none", "on", "off"}},
		{"explicit_model", c.Questions.ExplicitModel, []string{"none", "model"}},
	} {
		if q.r == nil {
			continue
		}
		for id, o := range q.r.Options {
			switch {
			case !contains(q.ids, id):
				errf("questions.%s: unknown option %s (want %v)", q.name, id, q.ids)
			case o == nil || o.What == "":
				errf("questions.%s.options.%s: what is required", q.name, id)
			case q.name == "explicit_model" && id == "model" && !strings.Contains(o.What, "{v}"):
				errf("questions.explicit_model.options.model.what must contain {v} (the model)")
			}
		}
	}
	for _, t := range []struct {
		name string
		v    float64
	}{{"relation_separate_threshold", m.RelationSeparateP}, {"detour_offer_threshold", m.DetourOfferP}, {"explicit_threshold", m.ExplicitP}, {"explicit_model_threshold", m.ExplicitModelP}} {
		if t.v < 0 || t.v > 1 {
			errf("meta.%s must be between 0 and 1", t.name)
		}
	}
	for _, d := range []struct {
		name string
		set  bool
	}{
		{"meta.continues_threshold", m.ContinuesThresholdP != 0}, {"meta.informs_threshold", m.InformsThresholdP != 0},
		{"questions.continues", c.Questions.Continues != nil}, {"questions.informs", c.Questions.Informs != nil},
	} {
		if d.set {
			warnf("%s: deprecated, ignored (the relation question replaced it)", d.name)
		}
	}
	for sc := range c.Questions.Level {
		if sc != ScopeMain && sc != ScopeSubagent {
			errf("questions.level.%s: unknown scope", sc)
		}
	}
	if c.State.RecentPrompts < 0 || c.State.RecentPrompts > 20 || c.State.LastAssistantTokens < 0 || c.State.LastAssistantTokens > 8000 {
		errf("state: recent_prompts must be in [0, 20] and last_assistant_tokens in [0, 8000]")
	}

	for _, id := range sortedKeys(c.Modes) {
		md := c.Modes[id]
		where := "mode " + id
		if len(md.Scopes) == 0 {
			errf("%s: scopes is empty", where)
		}
		for _, sc := range md.Scopes {
			if !contains(Scopes, sc) {
				errf("%s: unknown scope %q", where, sc)
			} else if md.MinTier != "" && c.Tier(sc, md.MinTier) == nil {
				errf("%s: min_tier %q is not a %s tier", where, md.MinTier, sc)
			}
		}
		if md.Threshold <= 0 || md.Threshold > 1 {
			errf("%s: threshold must be in (0, 1]", where)
		}
		if md.Question == "" || md.Yes == "" || md.No == "" {
			errf("%s: question, yes and no are required", where)
		}
		if md.Effort != "" && !contains(effortOrder, md.Effort) {
			errf("%s: unknown effort %q", where, md.Effort)
		}
	}
	for scope, tiers := range c.Tiers {
		for id, t := range tiers {
			if c.Model(t.Model) != nil && c.TierCost(t) == 0 {
				warnf("tier %s.%s: no measurement for %s@%s and no cost: its cost is interpolated", scope, id, t.Model, t.Effort)
			}
		}
	}
	// The policy reads rank as capability and cost as its price: a tier that
	// ranks above another but costs less makes the lower one pointless, and
	// a pick of the lower one for the higher one's work would count as an
	// overprovision instead of the underprovision it is.
	for _, scope := range sortedKeys(c.Tiers) {
		var prev *Tier
		for _, t := range c.TiersByRank(scope) {
			cost := c.TierCost(t)
			if cost <= 0 {
				continue
			}
			if prev != nil && cost < c.TierCost(prev) {
				warnf("tier %s.%s ranks above %s but costs less (%.2f vs %.2f): costs must rise with rank", scope, t.ID, prev.ID, cost, c.TierCost(prev))
			}
			prev = t
		}
	}
	if m.UnderprovisionPenalty < 0 {
		errf("meta.underprovision_penalty must be >= 0")
	}

	version := m.BenchmarkVersion
	dom := c.Dominance(version)
	for _, scope := range sortedKeys(c.Tiers) {
		for _, id := range sortedKeys(c.Tiers[scope]) {
			t := c.Tiers[scope][id]
			if by := dom.Dominators[ConfigKey(t.Model, t.Effort)]; len(by) > 0 {
				warnf("tier %s.%s: config %s is dominated by %v", scope, id, ConfigKey(t.Model, t.Effort), by)
			}
		}
	}
	for _, d := range dom.Duplicates {
		warnf("measurement %s listed twice for %s; the last one wins", d, version)
	}
	for _, id := range sortedKeys(c.Models) {
		if c.Models[id].Status == "active" && !dom.Measured[id] {
			warnf("model %s: active without a measurement in benchmark_version %s", id, version)
		}
	}
	for _, ms := range c.Measurements {
		md := c.Models[ms.Model]
		if md == nil {
			warnf("measurement %s: unknown model", ConfigKey(ms.Model, ms.Effort))
			continue
		}
		if ms.Effort != "" && len(md.Efforts) > 0 && !md.SupportsEffort(ms.Effort) {
			warnf("measurement %s: effort not in model efforts", ConfigKey(ms.Model, ms.Effort))
		}
		if ms.BenchmarkVersion == version && olderThan(ms.MeasuredAt, now, staleDays) {
			warnf("measurement %s: measured_at %s is older than %d days", ConfigKey(ms.Model, ms.Effort), ms.MeasuredAt, staleDays)
		}
	}
	if olderThan(m.LastRefresh, now, staleDays) {
		warnf("meta.last_refresh %s is older than %d days: run the refresh-model-catalog skill", m.LastRefresh, staleDays)
	}
	return is
}

// ConfigKey names a (model, effort) configuration.
func ConfigKey(model, effort string) string {
	if effort == "" {
		return model
	}
	return model + "@" + effort
}

type DominanceResult struct {
	Version    string
	Configs    []Measurement       // measurements of Version, one per config, sorted by cost
	Dominators map[string][]string // config -> configs dominating it
	Measured   map[string]bool     // model IDs with at least one measurement
	Duplicates []string
}

// Dominance compares configs measured in one benchmark version. A dominates B
// when it is at least as good on every shared dimension (index up, cost down,
// time down when both have it) and strictly better on one.
func (c *Catalog) Dominance(version string) DominanceResult {
	r := DominanceResult{Version: version, Dominators: map[string][]string{}, Measured: map[string]bool{}}
	idx := map[string]int{}
	for _, ms := range c.Measurements {
		if ms.BenchmarkVersion != version {
			continue
		}
		k := ConfigKey(ms.Model, ms.Effort)
		r.Measured[ms.Model] = true
		if i, ok := idx[k]; ok {
			r.Duplicates = append(r.Duplicates, k)
			r.Configs[i] = ms
			continue
		}
		idx[k] = len(r.Configs)
		r.Configs = append(r.Configs, ms)
	}
	sort.SliceStable(r.Configs, func(i, j int) bool { return r.Configs[i].CostPerTask < r.Configs[j].CostPerTask })
	for _, b := range r.Configs {
		for _, a := range r.Configs {
			if a.Model == b.Model && a.Effort == b.Effort {
				continue
			}
			if Dominates(a, b) {
				k := ConfigKey(b.Model, b.Effort)
				r.Dominators[k] = append(r.Dominators[k], ConfigKey(a.Model, a.Effort))
			}
		}
	}
	return r
}

func Dominates(a, b Measurement) bool {
	if a.Index < b.Index || a.CostPerTask > b.CostPerTask {
		return false
	}
	strict := a.Index > b.Index || a.CostPerTask < b.CostPerTask
	if a.TimePerTaskS != nil && b.TimePerTaskS != nil {
		if *a.TimePerTaskS > *b.TimePerTaskS {
			return false
		}
		strict = strict || *a.TimePerTaskS < *b.TimePerTaskS
	}
	return strict
}

func olderThan(date string, now time.Time, days int) bool {
	if date == "" {
		return false
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return false
	}
	return now.Sub(t) > time.Duration(days)*24*time.Hour
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
