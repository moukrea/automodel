// Package catalog loads, validates and hot-reloads catalog.toml, the single
// source of truth for models, prices, measurements and routing tiers.
package catalog

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	ScopeMain     = "main"
	ScopeSubagent = "subagent"
)

var Scopes = []string{ScopeMain, ScopeSubagent}

var Statuses = []string{"active", "dominated", "excluded", "retired"}

type Catalog struct {
	Meta         Meta                        `toml:"meta" json:"meta"`
	Models       map[string]*Model           `toml:"models" json:"models"`
	Measurements []Measurement               `toml:"measurements" json:"measurements"`
	Tiers        map[string]map[string]*Tier `toml:"tiers" json:"tiers"`
	Modes        map[string]*Mode            `toml:"modes" json:"modes,omitempty"`
	// Questions and State tune what Jev is asked and what it is shown;
	// anything left out uses automodel's built-in wording and sizes.
	Questions Questions   `toml:"questions" json:"questions,omitempty"`
	State     StateTuning `toml:"state" json:"state,omitempty"`
}

// Questions overrides the wording of the routing questions.
type Questions struct {
	// Level is the Score question's instructions, per scope.
	Level map[string]string `toml:"level" json:"level,omitempty"`
	// Relation is the Choice on how a main-session prompt relates to the
	// work in progress (asked once there is one).
	Relation *Relation `toml:"relation" json:"relation,omitempty"`
	// Explicit words the yes/no asked for each request a prompt's words may
	// make (an effort, more thinking, a mode, a model).
	Explicit *Explicit `toml:"explicit" json:"explicit,omitempty"`
	// Offer is the yes/no asked of a bare go-ahead after a detour whose
	// paused work needs more: did the assistant offer one more thing for
	// the detour, which the go-ahead accepts? (Else the go-ahead goes back
	// to the paused work.)
	Offer *Noul `toml:"offer" json:"offer,omitempty"`
	// Continues and Informs are the warm-turn yes/no questions the relation
	// question replaced: still parsed (older custom tunings), never asked.
	Continues *Noul `toml:"continues" json:"continues,omitempty"`
	Informs   *Noul `toml:"informs" json:"informs,omitempty"`
}

// Noul is a yes/no question: the question and each side's description.
type Noul struct {
	Question string `toml:"question" json:"question"`
	Yes      string `toml:"yes" json:"yes"`
	No       string `toml:"no" json:"no"`
}

// Relations are the options of the relation question. new_task, wrap_up
// and aside are separate from the work in progress (the prompt gets its
// own level; an aside, a question unrelated to the work, for that turn
// only); the others follow it up (they keep at least its level), and
// resume goes back to the work a detour paused (only offered when there is
// one).
var Relations = []string{RelationContinue, RelationExtend, RelationInform, RelationSideQuestion, RelationAside, RelationResume, RelationWrapUp, RelationNewTask}

const (
	RelationContinue     = "continue"
	RelationExtend       = "extend"
	RelationInform       = "inform"
	RelationSideQuestion = "side_question"
	RelationAside        = "aside"
	RelationResume       = "resume"
	RelationWrapUp       = "wrap_up"
	RelationNewTask      = "new_task"
)

// Separate reports whether a relation is separate from the work in progress.
func Separate(relation string) bool {
	return relation == RelationWrapUp || relation == RelationNewTask || relation == RelationAside
}

// Relation is the relation question: its instructions and one option per
// relation.
type Relation struct {
	Question string             `toml:"question" json:"question"`
	Options  map[string]*Option `toml:"options" json:"options"`
}

// Option is one option of a Choice in TypeSafe's advanced structure: what
// it is for, what it is not for, and examples that look like real inputs.
type Option struct {
	What     string   `toml:"what" json:"what"`
	NotFor   string   `toml:"not_for" json:"not_for,omitempty"`
	Examples []string `toml:"examples" json:"examples,omitempty"`
}

// Explicit words the explicit-request questions. {x} in a question names
// what the prompt may ask for, built from the names ({v}: the effort, the
// mode or the model). Anything left empty uses the built-in wording.
type Explicit struct {
	Question    string `toml:"question" json:"question,omitempty"`
	Yes         string `toml:"yes" json:"yes,omitempty"`
	No          string `toml:"no" json:"no,omitempty"`
	OffQuestion string `toml:"off_question" json:"off_question,omitempty"` // a mode the prompt may refuse
	OffYes      string `toml:"off_yes" json:"off_yes,omitempty"`
	OffNo       string `toml:"off_no" json:"off_no,omitempty"`
	// A model: the one the assistant itself runs on (models are talked
	// about far more often than asked for).
	ModelQuestion string `toml:"model_question" json:"model_question,omitempty"`
	ModelYes      string `toml:"model_yes" json:"model_yes,omitempty"`
	ModelNo       string `toml:"model_no" json:"model_no,omitempty"`
	Effort        string `toml:"effort" json:"effort,omitempty"`
	More          string `toml:"more" json:"more,omitempty"`
	Mode          string `toml:"mode" json:"mode,omitempty"`
	Model         string `toml:"model" json:"model,omitempty"`
}

// StateTuning sizes what the main session's state shows Jev.
type StateTuning struct {
	RecentPrompts       int `toml:"recent_prompts" json:"recent_prompts,omitempty"`               // default 5
	LastAssistantTokens int `toml:"last_assistant_tokens" json:"last_assistant_tokens,omitempty"` // default 1500
}

// RecentPromptsN is state.recent_prompts or 5.
func (s StateTuning) RecentPromptsN() int {
	if s.RecentPrompts > 0 {
		return s.RecentPrompts
	}
	return 5
}

// LastAssistantN is state.last_assistant_tokens or 1500.
func (s StateTuning) LastAssistantN() int {
	if s.LastAssistantTokens > 0 {
		return s.LastAssistantTokens
	}
	return 1500
}

type Meta struct {
	Schema              int    `toml:"schema" json:"schema"`
	LastRefresh         string `toml:"last_refresh" json:"last_refresh"`
	Benchmark           string `toml:"benchmark" json:"benchmark"`
	BenchmarkVersion    string `toml:"benchmark_version" json:"benchmark_version"`
	JevModel            string `toml:"jev_model" json:"jev_model"`
	DefaultMainTier     string `toml:"default_main_tier" json:"default_main_tier"`
	DefaultSubagentTier string `toml:"default_subagent_tier" json:"default_subagent_tier"`
	// LongContextBeta is the anthropic-beta value that unlocks windows above
	// StandardContext on models with long_context = "beta". The proxy sends
	// it for those and strips it everywhere else.
	LongContextBeta string `toml:"long_context_beta" json:"long_context_beta"`
	// MainMinContext is the smallest window a main-session tier may have:
	// routing a long session onto a smaller window breaks it.
	MainMinContext int `toml:"main_min_context" json:"main_min_context"`
	// UnderprovisionPenalty weighs working below the right tier against
	// working above it: too little effort costs this many times the cost
	// gap (rework, wrong answers), too much effort costs the gap once.
	UnderprovisionPenalty float64 `toml:"underprovision_penalty" json:"underprovision_penalty,omitempty"`
	// RelationSeparateP is the probability of the separate relations
	// (new_task + wrap_up) from which a prompt gets its own level, below the
	// work in progress if that is its level; under it the prompt follows the
	// work up and keeps at least its level.
	RelationSeparateP float64 `toml:"relation_separate_threshold" json:"relation_separate_threshold,omitempty"`
	// DetourOfferP is the yes-probability of the offer question from which
	// a go-ahead after a detour takes up what the assistant offered for the
	// detour (a wrap-up step of it, more of it) when the paused work needs
	// more; under it the go-ahead goes back to the paused work, as a bare
	// go-ahead does.
	DetourOfferP float64 `toml:"detour_offer_threshold" json:"detour_offer_threshold,omitempty"`
	// ExplicitP is the yes-probability from which a request in the prompt's
	// words (an effort, more thinking, a mode) counts; ExplicitModelP the
	// one for a model, stricter (a model is mentioned far more often than
	// asked for, and a wrong one runs the whole work).
	ExplicitP      float64 `toml:"explicit_threshold" json:"explicit_threshold,omitempty"`
	ExplicitModelP float64 `toml:"explicit_model_threshold" json:"explicit_model_threshold,omitempty"`
	// ContinuesThresholdP and InformsThresholdP belonged to the questions
	// the relation question replaced: parsed, ignored.
	ContinuesThresholdP float64 `toml:"continues_threshold" json:"continues_threshold,omitempty"`
	InformsThresholdP   float64 `toml:"informs_threshold" json:"informs_threshold,omitempty"`
	// PerTurnEffortBeta is the anthropic-beta value for per-turn effort.
	PerTurnEffortBeta string `toml:"per_turn_effort_beta" json:"per_turn_effort_beta,omitempty"`
}

// StandardContext is the window every model gets without the long-context beta.
const StandardContext = 200_000

// DefaultMainMinContext applies when meta.main_min_context is unset.
const DefaultMainMinContext = 1_000_000

// RelationSeparateThreshold returns meta.relation_separate_threshold or
// 0.6 (TypeSafe's top-probability gate for a Choice: 99% agreement with
// the labels above it in their self-consistency cookbook).
func (m Meta) RelationSeparateThreshold() float64 {
	if m.RelationSeparateP > 0 {
		return m.RelationSeparateP
	}
	return 0.6
}

// DetourOfferThreshold returns meta.detour_offer_threshold or 0.8.
func (m Meta) DetourOfferThreshold() float64 {
	if m.DetourOfferP > 0 {
		return m.DetourOfferP
	}
	return 0.8
}

// ExplicitThreshold returns meta.explicit_threshold or 0.8 (probe on jev-1.13:
// requests 0.95, mentions 0.06, refusals 0.03).
func (m Meta) ExplicitThreshold() float64 {
	if m.ExplicitP > 0 {
		return m.ExplicitP
	}
	return 0.8
}

// ExplicitModelThreshold returns meta.explicit_model_threshold or 0.9.
func (m Meta) ExplicitModelThreshold() float64 {
	if m.ExplicitModelP > 0 {
		return m.ExplicitModelP
	}
	return 0.9
}

// MinMainContext returns meta.main_min_context or its default.
func (m Meta) MinMainContext() int {
	if m.MainMinContext > 0 {
		return m.MainMinContext
	}
	return DefaultMainMinContext
}

// NeedsLongContextBeta reports whether requests to md need the beta for its
// full window. Models with a native long window must not get it (it defeats
// the prompt cache), and smaller models reject it.
func (c *Catalog) NeedsLongContextBeta(md *Model) bool {
	return md != nil && md.Context > StandardContext && md.LongContext == "beta" && c.Meta.LongContextBeta != ""
}

type Model struct {
	ID      string `toml:"-" json:"id"`
	Label   string `toml:"label" json:"label"`
	Status  string `toml:"status" json:"status"`
	Reason  string `toml:"reason" json:"reason,omitempty"`
	APIID   string `toml:"api_id" json:"api_id,omitempty"`
	Alias   string `toml:"alias" json:"alias,omitempty"`
	Context int    `toml:"context" json:"context,omitempty"`
	// LongContext says how a window above StandardContext is obtained:
	// "native" (always available) or "beta" (meta.long_context_beta).
	LongContext string `toml:"long_context" json:"long_context,omitempty"`
	// MaxOutput is the model's output-token ceiling (routed requests are
	// raised to it: Claude Code caps unknown models lower).
	MaxOutput int      `toml:"max_output" json:"max_output,omitempty"`
	Efforts   []string `toml:"efforts" json:"efforts"`
	// PerTurnEffort: the model accepts effort-only system messages mid
	// conversation (meta.per_turn_effort_beta), so an effort change keeps
	// the prompt cache.
	PerTurnEffort bool     `toml:"per_turn_effort" json:"per_turn_effort,omitempty"`
	DefaultEffort string   `toml:"default_effort" json:"default_effort,omitempty"`
	Price         *Price   `toml:"price" json:"price,omitempty"`
	Fast          *Fast    `toml:"fast" json:"fast,omitempty"`
	Scopes        []string `toml:"scopes" json:"scopes,omitempty"`
	VerifiedAt    string   `toml:"verified_at" json:"verified_at,omitempty"`
	Sources       []string `toml:"sources" json:"sources,omitempty"`
}

type Price struct {
	Input        float64 `toml:"input" json:"input"`
	Output       float64 `toml:"output" json:"output"`
	CacheRead    float64 `toml:"cache_read" json:"cache_read"`
	CacheWrite5m float64 `toml:"cache_write_5m" json:"cache_write_5m"`
	CacheWrite1h float64 `toml:"cache_write_1h" json:"cache_write_1h"`
}

type Fast struct {
	Allowed bool    `toml:"allowed" json:"allowed"`
	Input   float64 `toml:"input" json:"input"`
	Output  float64 `toml:"output" json:"output"`
	Speedup float64 `toml:"speedup" json:"speedup"`
}

type Measurement struct {
	Model               string   `toml:"model" json:"model"`
	Effort              string   `toml:"effort" json:"effort"`
	Index               float64  `toml:"index" json:"index"`
	CostPerTask         float64  `toml:"cost_per_task" json:"cost_per_task"`
	TimePerTaskS        *float64 `toml:"time_per_task_s" json:"time_per_task_s,omitempty"`
	OutputTokensPerTask *float64 `toml:"output_tokens_per_task" json:"output_tokens_per_task,omitempty"`
	BenchmarkVersion    string   `toml:"benchmark_version" json:"benchmark_version"`
	MeasuredAt          string   `toml:"measured_at" json:"measured_at"`
	Source              string   `toml:"source" json:"source"`
}

type Tier struct {
	ID     string `toml:"-" json:"id"`
	Scope  string `toml:"-" json:"scope"`
	Rank   int    `toml:"rank" json:"rank"`
	Model  string `toml:"model" json:"model"`
	Effort string `toml:"effort" json:"effort,omitempty"`
	// Criteria describes the situations this tier is for (one level of the
	// Jev Score question): situations, not degrees.
	Criteria string `toml:"criteria" json:"criteria"`
	// Cost is the relative cost per task, used when the benchmark has no
	// measurement for this model and effort.
	Cost float64 `toml:"cost" json:"cost,omitempty"`
	// MaxContext: the tier is only used while the session's context stays
	// at or below this many tokens. A main tier whose model has a smaller
	// window than meta.main_min_context must set it (the session then
	// moves to a bigger model before the window is reached).
	MaxContext int `toml:"max_context" json:"max_context,omitempty"`
	// Question makes this an asked tier: it is not a level of Jev's Score
	// question but a yes/no of its own (Criteria is the yes side, No the
	// other), and it replaces the scored tier ranked just above it when
	// Jev's yes-probability reaches Threshold.
	Question  string  `toml:"question" json:"question,omitempty"`
	No        string  `toml:"no" json:"no,omitempty"`
	Threshold float64 `toml:"threshold" json:"threshold,omitempty"`
}

// Asked reports whether the tier is chosen by its own question.
func (t *Tier) Asked() bool { return t.Question != "" }

// Fits reports whether the tier can serve a context of n tokens (n <= 0:
// unknown, it fits).
func (c *Catalog) Fits(t *Tier, n int) bool {
	if n <= 0 {
		return true
	}
	if t.MaxContext > 0 && n > t.MaxContext {
		return false
	}
	m := c.Model(t.Model)
	return m == nil || m.Context >= n
}

// Mode is a way of working layered on top of a tier, whatever its model:
// ultracode is parallel multi-agent orchestration (workflows), and can run
// on any main model. Jev answers a yes/no question for it.
type Mode struct {
	ID     string   `toml:"-" json:"id"`
	Scopes []string `toml:"scopes" json:"scopes"`
	// Workflows: the mode turns on workflow orchestration (Claude Code's
	// ultracode opt-in notice).
	Workflows bool `toml:"workflows" json:"workflows"`
	// Effort is the minimum effort while the mode is on.
	Effort string `toml:"effort" json:"effort,omitempty"`
	// MinTier: the mode only applies on top of this tier or a higher one.
	MinTier string `toml:"min_tier" json:"min_tier,omitempty"`
	// Threshold is the yes-probability Jev must give.
	Threshold float64 `toml:"threshold" json:"threshold"`
	// Question, Yes and No are the Noul question and what counts as yes/no.
	Question string `toml:"question" json:"question"`
	Yes      string `toml:"yes" json:"yes"`
	No       string `toml:"no" json:"no"`
}

// Issue is a validation finding. Errors reject the catalog; warnings don't.
type Issue struct {
	Level   string `json:"level"` // "error" | "warning"
	Message string `json:"message"`
}

func (i Issue) String() string { return i.Level + ": " + i.Message }

type Issues []Issue

func (is Issues) Errors() Issues   { return is.filter("error") }
func (is Issues) Warnings() Issues { return is.filter("warning") }
func (is Issues) filter(l string) Issues {
	var out Issues
	for _, i := range is {
		if i.Level == l {
			out = append(out, i)
		}
	}
	return out
}

// Parse decodes a catalog without validating it.
func Parse(data []byte) (*Catalog, error) {
	var c Catalog
	if _, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&c); err != nil {
		return nil, err
	}
	for id, m := range c.Models {
		if m == nil {
			m = &Model{}
			c.Models[id] = m
		}
		m.ID = id
	}
	for id, m := range c.Modes {
		if m == nil {
			m = &Mode{}
			c.Modes[id] = m
		}
		m.ID = id
	}
	for scope, tiers := range c.Tiers {
		for id, t := range tiers {
			if t == nil {
				t = &Tier{}
				tiers[id] = t
			}
			t.ID, t.Scope = id, scope
		}
	}
	return &c, nil
}

// Load reads, parses and validates a catalog. The catalog is returned even
// when it has errors so callers can report them; use Issues.Errors() to gate.
func Load(path string, now time.Time, staleDays int) (*Catalog, Issues, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, c.Validate(now, staleDays), nil
}

func (c *Catalog) Model(id string) *Model { return c.Models[id] }

// Tier returns a tier by scope and ID, or nil.
func (c *Catalog) Tier(scope, id string) *Tier { return c.Tiers[scope][id] }

// TiersByRank returns a scope's tiers sorted by ascending rank.
func (c *Catalog) TiersByRank(scope string) []*Tier {
	var out []*Tier
	for _, t := range c.Tiers[scope] {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// ScoredTiers returns the levels of Jev's Score question: the scope's tiers
// by rank, without asked tiers.
func (c *Catalog) ScoredTiers(scope string) []*Tier {
	var out []*Tier
	for _, t := range c.TiersByRank(scope) {
		if !t.Asked() {
			out = append(out, t)
		}
	}
	return out
}

// AskedAbove returns the asked tier that stands in for scored tier t (the
// asked tier ranked just below it), or nil.
func (c *Catalog) AskedAbove(scope string, t *Tier) *Tier {
	var prev *Tier
	for _, x := range c.TiersByRank(scope) {
		if x.ID == t.ID {
			if prev != nil && prev.Asked() {
				return prev
			}
			return nil
		}
		prev = x
	}
	return nil
}

// ModesFor returns the modes available in a scope, sorted by ID.
func (c *Catalog) ModesFor(scope string) []*Mode {
	var out []*Mode
	for _, m := range c.Modes {
		if contains(m.Scopes, scope) {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// TierCost is a tier's relative cost per task: the benchmark measurement of
// its model and effort, else the tier's own cost, else 0 (unknown).
func (c *Catalog) TierCost(t *Tier) float64 {
	for _, m := range c.Measurements {
		if m.Model == t.Model && m.Effort == t.Effort && m.BenchmarkVersion == c.Meta.BenchmarkVersion && m.CostPerTask > 0 {
			return m.CostPerTask
		}
	}
	return t.Cost
}

// Resolve returns what a tier plus an optional mode runs as: the tier's
// model, its effort raised to the mode's minimum, and whether workflows are on.
func (c *Catalog) Resolve(t *Tier, modeID string) (model, effort string, workflows bool) {
	model, effort = t.Model, t.Effort
	if md := c.Modes[modeID]; md != nil {
		workflows = md.Workflows
		if m := c.Model(t.Model); md.Effort != "" && m != nil && m.SupportsEffort(md.Effort) && EffortRank(md.Effort) > EffortRank(effort) {
			effort = md.Effort
		}
	}
	return model, effort, workflows
}

// KeepsMode reports whether a mode can stay on at an effort the user chose:
// not below the mode's own effort, which the mode would raise it to.
func (c *Catalog) KeepsMode(modeID, effort string) bool {
	md := c.Modes[modeID]
	return md != nil && (md.Effort == "" || EffortRank(effort) >= EffortRank(md.Effort))
}

// TierFor returns the scope's tier running model at effort, if any.
func (c *Catalog) TierFor(scope, model, effort string) *Tier {
	for _, t := range c.TiersByRank(scope) {
		if t.Model == model && t.Effort == effort {
			return t
		}
	}
	return nil
}

// DefaultTier returns the fallback tier of a scope.
func (c *Catalog) DefaultTier(scope string) *Tier {
	if scope == ScopeSubagent {
		return c.Tier(scope, c.Meta.DefaultSubagentTier)
	}
	return c.Tier(scope, c.Meta.DefaultMainTier)
}

// ModelByAPIID finds a model by the ID sent to the API.
func (c *Catalog) ModelByAPIID(apiID string) *Model {
	for _, m := range c.Models {
		if m.APIID == apiID {
			return m
		}
	}
	return nil
}

// ShortLabel is the compact form used by the statusline: "Opus 5.5" -> "opus-5.5".
func (m *Model) ShortLabel() string {
	l := m.Label
	if l == "" {
		l = m.ID
	}
	return strings.ToLower(strings.ReplaceAll(l, " ", "-"))
}

func (m *Model) SupportsEffort(e string) bool { return contains(m.Efforts, e) }

func (m *Model) AllowedIn(scope string) bool {
	return len(m.Scopes) == 0 || contains(m.Scopes, scope)
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
