// Package jev calls the OpenRouter Decisions API (TypeSafe Jev) with the
// routing questions generated from the catalog.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
)

type Client struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}

type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
	SessionID string              `json:"session_id,omitempty"`
}

// Question is a Choice (criteria: option -> description), a Score
// (criteria: ordered level descriptions) or a Noul (criteria: true/false).
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer holds any answer type: Choice (Choice), Score (Score, keyed
// probabilities "0".."n"), Noul (Noul, the yes-probability).
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Noul          *float64           `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type Response struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens int     `json:"input_tokens"`
		Cost        float64 `json:"cost"`
	} `json:"usage"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// QuestionName is the key of the single question we ask.
const QuestionName = "tier"

var instructions = map[string]string{
	catalog.ScopeMain:     "Which reasoning tier should handle the rest of this Claude Code session?",
	catalog.ScopeSubagent: "Which model and reasoning tier should handle this Claude Code subagent task?",
}

// TierQuestion builds the v1 Choice from the catalog: one option per tier.
// Kept for `automodel eval --format choice`.
func TierQuestion(c *catalog.Catalog, scope string) Question {
	crit := map[string]string{}
	for _, t := range c.ScoredTiers(scope) {
		crit[t.ID] = t.Criteria
	}
	return Question{Type: "choice", Instructions: instructions[scope], Criteria: crit}
}

// Question IDs of a routing request.
const (
	QLevel       = "level"
	QRelation    = "relation"
	QExplicitPfx = "explicit_"
	QTierPfx     = "tier_"
	QModePfx     = "mode_"
)

// DefaultLevel is the Score question's built-in instructions, per scope
// (the catalog's questions.level overrides them).
var DefaultLevel = map[string]string{
	catalog.ScopeMain:     "How much reasoning does the work that the new prompt starts need, judging by what that work actually involves (a short prompt can approve a large job)? When the prompt hands work to subagents, judge only what this session does itself (launching them, then relaying or merging their reports): each subagent gets its own level.",
	catalog.ScopeSubagent: "How much capability and reasoning does this subagent task need?",
}

// Ask says which of the optional questions a request carries.
type Ask struct {
	// Relation: how the prompt relates to the work in progress (main
	// session, once there is work in progress). Resume offers going back
	// to the work a detour paused (once there is one).
	Relation, Resume bool
	// Explicit: the requests a regex found in the prompt's words, a yes/no
	// each (Jev tells a request from a mention).
	Explicit []Explicit
}

// Explicit is a request a prompt's words may make: an effort ("more": more
// thinking), a mode ("off": the mode refused), a model.
type Explicit struct {
	Kind  string // effort | mode | model
	Value string // low..max or more; the mode's ID or off; the model's catalog key
	Label string // what the question calls it: the effort, the mode's ID, the model's label
}

// Kinds and special values of explicit requests.
const (
	ExplicitEffort = "effort"
	ExplicitMode   = "mode"
	ExplicitModel  = "model"
	ExplicitMore   = "more"
	ExplicitOff    = "off"
)

// ID is the request's question ID: explicit_effort_xhigh, explicit_mode_off...
func (x Explicit) ID() string { return QExplicitPfx + x.Kind + "_" + x.Value }

// Questions builds the routing questions of a scope: a Score over the tiers
// (they are ordered, so a Score fits better than a Choice), one Noul per
// mode and per asked tier, and what a asks for: a Choice on the prompt's
// relation to the work in progress, a Noul per explicit request found in
// the prompt. It returns the tier IDs in level order.
func Questions(c *catalog.Catalog, scope string, a Ask) (map[string]Question, []string) {
	var levels, ids []string
	for _, t := range c.ScoredTiers(scope) {
		levels, ids = append(levels, t.Criteria), append(ids, t.ID)
	}
	level := DefaultLevel[scope]
	if v := c.Questions.Level[scope]; v != "" {
		level = v
	}
	qs := map[string]Question{QLevel: {Type: "score", Instructions: level, Criteria: levels}}
	for _, t := range c.TiersByRank(scope) {
		if t.Asked() {
			qs[QTierPfx+t.ID] = Question{Type: "noul", Instructions: t.Question, Criteria: map[string]string{"true": t.Criteria, "false": t.No}}
		}
	}
	for _, m := range c.ModesFor(scope) {
		qs[QModePfx+m.ID] = Question{Type: "noul", Instructions: m.Question, Criteria: map[string]string{"true": m.Yes, "false": m.No}}
	}
	if a.Relation && scope == catalog.ScopeMain {
		qs[QRelation] = RelationQuestion(c, a.Resume)
	}
	for _, x := range a.Explicit {
		qs[x.ID()] = ExplicitQuestion(c, x)
	}
	return qs, ids
}

// RelationQuestion is the Choice on how the prompt relates to the work in
// progress: the catalog's wording (questions.relation) over the built-in
// one, each option an object {what, not_for, examples}. The resume option
// is only offered when there is paused work to go back to.
func RelationQuestion(c *catalog.Catalog, resume bool) Question {
	q, opts := DefaultRelation.Question, map[string]*catalog.Option{}
	r := c.Questions.Relation
	if r != nil && r.Question != "" {
		q = r.Question
	}
	for _, id := range catalog.Relations {
		if id == catalog.RelationResume && !resume {
			continue
		}
		opts[id] = DefaultRelation.Options[id]
		if r != nil && r.Options[id] != nil && r.Options[id].What != "" {
			opts[id] = r.Options[id]
		}
	}
	return Question{Type: "choice", Instructions: q, Criteria: opts}
}

// ExplicitQuestion is the yes/no that confirms one explicit request.
func ExplicitQuestion(c *catalog.Catalog, x Explicit) Question {
	w := DefaultExplicit
	if o := c.Questions.Explicit; o != nil {
		for _, f := range []struct {
			dst *string
			v   string
		}{
			{&w.Question, o.Question}, {&w.Yes, o.Yes}, {&w.No, o.No},
			{&w.OffQuestion, o.OffQuestion}, {&w.OffYes, o.OffYes}, {&w.OffNo, o.OffNo},
			{&w.ModelQuestion, o.ModelQuestion}, {&w.ModelYes, o.ModelYes}, {&w.ModelNo, o.ModelNo},
			{&w.Effort, o.Effort}, {&w.More, o.More}, {&w.Mode, o.Mode}, {&w.Model, o.Model},
		} {
			if f.v != "" {
				*f.dst = f.v
			}
		}
	}
	name := map[string]string{ExplicitEffort: w.Effort, ExplicitMode: w.Mode, ExplicitModel: w.Model}[x.Kind]
	if x.Kind == ExplicitEffort && x.Value == ExplicitMore {
		name = w.More
	}
	name = strings.ReplaceAll(name, "{v}", x.Label)
	q, yes, no := w.Question, w.Yes, w.No
	switch {
	case x.Kind == ExplicitMode && x.Value == ExplicitOff:
		q, yes, no = w.OffQuestion, w.OffYes, w.OffNo
	case x.Kind == ExplicitModel:
		q, yes, no = w.ModelQuestion, w.ModelYes, w.ModelNo
	}
	return Question{Type: "noul", Instructions: strings.ReplaceAll(q, "{x}", name), Criteria: map[string]string{"true": yes, "false": no}}
}

// DefaultRelation and DefaultExplicit are the built-in wordings (the
// catalog's questions.relation and questions.explicit override them).
var (
	DefaultRelation = catalog.Relation{
		Question: "How does the new prompt `task` relate to the work in progress (started by `work_in_progress.goal`, carried on in `recent_prompts`, last reported in `last_assistant`)?",
		Options: map[string]*catalog.Option{
			catalog.RelationContinue: {
				What:     "Tells the assistant to go ahead with, keep going on or resume the work in progress as it stands, or to carry out what it just proposed or offered for that work (possibly picking one of its options), and asks for nothing more.",
				NotFor:   "A go-ahead that also adds or changes something (extend).",
				Examples: []string{"yes", "go", "continue", "ok ship it", "ok, do what you proposed", "go with option 2", "resume, the limits are reset", "vas-y", "oui, continue", "oui, fais ce que tu proposes", "c'est bon, on y va"},
			},
			catalog.RelationExtend: {
				What:     "Adds to, constrains or corrects the work in progress, which stays the same piece of work: another case or input to handle, a test for it, a requirement, a different approach.",
				NotFor:   "Work that stands on its own without the work in progress, including the same change repeated on another target (new_task).",
				Examples: []string{"also add a test for that", "and make it configurable", "but keep the old flag working", "no, use a channel instead", "ajoute aussi un log quand ça échoue", "mais garde l'ancienne API", "non, fais plutôt une migration"},
			},
			catalog.RelationInform: {
				What:     "Only gives a fact, a preference or an answer the work in progress needs, and asks for no new work.",
				NotFor:   "A message that also asks for a change or a check (extend).",
				Examples: []string{"FYI it only fails on ARM", "env vars win", "camelCase", "c'est la v2 de l'API", "la clé est dans le .env"},
			},
			catalog.RelationSideQuestion: {
				What:     "Asks a question or a quick check aside while the work in progress is still pending (steps remain, it is running, or a proposal awaits an answer), and only needs an answer: about that work (progress, status, a detail, a doubt) or about anything else (a command, a fact, a comparison).",
				NotFor:   "A question that starts real work of its own, such as an investigation or a change (new_task); a question about work that is finished, which only recalls or explains it (wrap_up).",
				Examples: []string{"is CI green yet?", "why did you pick a mutex there?", "which Go version do we target again?", "unrelated: how do I list open ports on macOS?", "t'en es où ?", "le build passe ?", "au fait, c'est quoi la différence entre rebase et merge ?"},
			},
			catalog.RelationResume: {
				What:     "Goes back to the paused work (`paused_work.goal`), which a detour set aside, now that the detour is done or dropped.",
				NotFor:   "Going on with the work in progress itself (continue), or a piece of work neither of them is about (new_task).",
				Examples: []string{"back to the migration", "ok, now let's get back to the refactor", "continue the audit", "reprends le refacto", "on revient à la migration", "bon, on reprend l'audit"},
			},
			catalog.RelationWrapUp: {
				What:     "Wraps up work that is finished: a summary or a recap, a commit message, a PR description, a push, a changelog entry, or a question that only recalls or explains the finished work (what changed, how it works, why it was done that way).",
				NotFor:   "Finishing or fixing the work itself (extend); a question while the work is still pending (side_question).",
				Examples: []string{"write the commit message", "summarize what you changed", "open the PR", "push it", "how does the new retry work, in two sentences?", "résume ce que tu as fait", "c'était quoi le problème, finalement ?", "fais le commit et pousse"},
			},
			catalog.RelationNewTask: {
				What:     "Starts a separate piece of work (a change, a fix, a feature, an investigation) that the work in progress doesn't include, even one that repeats its pattern on another target (another endpoint, page or module).",
				NotFor:   "More work on the work in progress itself (extend); carrying out what the assistant just proposed for it (continue); a question that only needs an answer while that work is pending (side_question).",
				Examples: []string{"now rename the config loader", "next: design how to shard the job queue", "now the same retry logic for the email sender", "unrelated, but the login page is slow", "autre chose : mets à jour le README", "passons au module de facturation"},
			},
		},
	}
	DefaultExplicit = catalog.Explicit{
		Question:      "Does the new prompt `task` explicitly ask the assistant to use {x} for this work?",
		Yes:           "It asks for it, as an instruction or a wish, in any language: 'do this at xhigh', 'use ultracode for the audit', 'switch to Sonnet', 'think harder about it', 'passe en low', 'fais-le en ultracode', 'réfléchis à fond'.",
		No:            "It only mentions, quotes, discusses or questions it, or refuses it: 'why did it stay at xhigh?', 'max retries is 3', 'the workflow failed', 'ultracode was slow', 'pas besoin de xhigh ici', 'le CPU tourne à fond'.",
		OffQuestion:   "Does the new prompt `task` explicitly ask the assistant to stop using {x}, or not to use it, for this work?",
		OffYes:        "It asks to stop it or to do without it, in any language: 'no ultracode for this', 'without workflows', 'stop the parallel agents', 'pas besoin d'ultracode', 'sans workflow', 'arrête les agents en parallèle'.",
		OffNo:         "It asks for it, only mentions it, or says nothing against it: 'use ultracode', 'did the workflow finish?', 'ultracode était lent hier'.",
		ModelQuestion: "Does the new prompt `task` ask the assistant to run on {x} itself for this work, instead of the model it runs on now?",
		ModelYes:      "It tells the assistant to switch to that model, or to do this work with it, as an instruction or a wish: 'switch to Sonnet for this', 'do the rest with Fable', 'use Opus for this part', 'passe sur Sonnet pour la suite', 'fais ça avec Fable'.",
		ModelNo:       "It only talks about the model or refuses it: a mention, a comparison, release news, prices or benchmarks, a question about models or about how automodel routes and why it picked one, a subagent, a config or a catalog entry set to it, a refusal: 'Sonnet 5.5 is out', 'is Sonnet cheaper than Opus?', 'why did it pick Opus?', 'Fable tops the index now', 'make the review agent use sonnet', 'pas besoin d'Opus', 'Opus a mis 3 minutes'.",
		Effort:        "the {v} reasoning effort",
		More:          "more thinking than so far (thinking harder, longer or more carefully)",
		Mode:          "the {v} mode (several agents working in parallel, orchestrated as a workflow)",
		Model:         "the {v} model",
	}
)

// LevelProbs maps a Score answer onto tier IDs (in level order).
func LevelProbs(a Answer, ids []string) map[string]float64 {
	out := make(map[string]float64, len(ids))
	for i, id := range ids {
		out[id] = a.Probabilities[fmt.Sprint(i)]
	}
	return out
}

// Ask sends several questions about one state in one call.
func (cl *Client) Ask(ctx context.Context, model, sessionID string, state any, qs map[string]Question) (map[string]Answer, *Response, error) {
	r, err := cl.do(ctx, Request{Model: model, State: state, SessionID: sessionID, Questions: qs})
	if err != nil {
		return nil, r, err
	}
	for id, q := range qs {
		a, ok := r.Answers[id]
		if !ok {
			return nil, r, fmt.Errorf("jev: no answer for %q", id)
		}
		if q.Type == "noul" && a.Noul == nil {
			return nil, r, fmt.Errorf("jev: %q: noul answer without a value", id)
		}
		if (q.Type == "score" || q.Type == "choice") && len(a.Probabilities) == 0 {
			return nil, r, fmt.Errorf("jev: %q: %s answer without probabilities", id, q.Type)
		}
	}
	return r.Answers, r, nil
}

var ErrNoKey = errors.New("OPENROUTER_API_KEY is not set")

// Decide asks one v1 tier Choice. The context carries the timeout.
func (cl *Client) Decide(ctx context.Context, model, sessionID string, state any, q Question) (*Answer, *Response, error) {
	r, err := cl.do(ctx, Request{Model: model, State: state, SessionID: sessionID, Questions: map[string]Question{QuestionName: q}})
	if err != nil {
		return nil, r, err
	}
	a, ok := r.Answers[QuestionName]
	if !ok || a.Choice == "" {
		return nil, r, fmt.Errorf("jev: no answer for %q", QuestionName)
	}
	if crit, _ := q.Criteria.(map[string]string); crit != nil {
		if _, known := crit[a.Choice]; !known {
			return nil, r, fmt.Errorf("jev: unknown choice %q", a.Choice)
		}
	}
	return &a, r, nil
}

func (cl *Client) do(ctx context.Context, reqBody Request) (*Response, error) {
	if cl.APIKey == "" {
		return nil, ErrNoKey
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cl.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cl.APIKey)
	req.Header.Set("Content-Type", "application/json")
	hc := cl.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var r Response
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("jev: status %d: %.200s", resp.StatusCode, data)
	}
	if r.Error != nil {
		return &r, fmt.Errorf("jev: %d: %s", r.Error.Code, r.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return &r, fmt.Errorf("jev: status %d", resp.StatusCode)
	}
	return &r, nil
}

// Ranked returns options by descending probability.
func (a *Answer) Ranked() []string {
	opts := make([]string, 0, len(a.Probabilities))
	for k := range a.Probabilities {
		opts = append(opts, k)
	}
	sort.Slice(opts, func(i, j int) bool {
		pi, pj := a.Probabilities[opts[i]], a.Probabilities[opts[j]]
		if pi != pj {
			return pi > pj
		}
		return opts[i] < opts[j]
	})
	return opts
}
