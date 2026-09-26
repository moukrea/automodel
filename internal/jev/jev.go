// Package jev calls the OpenRouter Decisions API (TypeSafe Jev) with a choice
// question generated from the catalog tiers of one scope.
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
	for _, t := range c.TiersByRank(scope) {
		crit[t.ID] = t.Criteria
	}
	return Question{Type: "choice", Instructions: instructions[scope], Criteria: crit}
}

// Question IDs of a routing request.
const (
	QLevel     = "level"
	QContinues = "continues"
	QModePfx   = "mode_"
)

var levelInstructions = map[string]string{
	catalog.ScopeMain:     "How much reasoning does the work that the new prompt starts need, judging by what that work actually involves (a short prompt can approve a large job)?",
	catalog.ScopeSubagent: "How much capability and reasoning does this subagent task need?",
}

// Questions builds the routing questions of a scope: a Score over the tiers
// (they are ordered, so a Score fits better than a Choice), one Noul per
// mode, and on warm turns a Noul on whether the prompt continues the work in
// progress. It returns the tier IDs in level order.
func Questions(c *catalog.Catalog, scope string, warm bool) (map[string]Question, []string) {
	var levels, ids []string
	for _, t := range c.TiersByRank(scope) {
		levels, ids = append(levels, t.Criteria), append(ids, t.ID)
	}
	qs := map[string]Question{QLevel: {Type: "score", Instructions: levelInstructions[scope], Criteria: levels}}
	for _, m := range c.ModesFor(scope) {
		qs[QModePfx+m.ID] = Question{Type: "noul", Instructions: m.Question, Criteria: map[string]string{"true": m.Yes, "false": m.No}}
	}
	if warm {
		qs[QContinues] = Question{Type: "noul", Instructions: "Does the new prompt keep the assistant on the work already in progress, at the same depth?",
			Criteria: map[string]string{
				"true":  "Go-ahead or continuation of the ongoing task: 'yes, do it', 'continue', answering the assistant's question, adding a constraint or a fix to what is being built.",
				"false": "A separate or smaller step: a new question or feature, a summary, a commit message or PR description, an explanation of what was done.",
			}}
	}
	return qs, ids
}

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
		if q.Type == "score" && len(a.Probabilities) == 0 {
			return nil, r, fmt.Errorf("jev: %q: score answer without probabilities", id)
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
