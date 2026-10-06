// Package ledger appends one JSON line per routing decision and per Anthropic
// response, and aggregates them into the calibration report.
package ledger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/flock"
)

type Shadow struct {
	Model      string             `json:"model"`
	Choice     string             `json:"choice,omitempty"`
	Confidence float64            `json:"confidence,omitempty"`
	Probs      map[string]float64 `json:"probs,omitempty"`
	Chosen     string             `json:"chosen,omitempty"`
	CostUSD    float64            `json:"jev_cost_usd,omitempty"`
	Error      string             `json:"error,omitempty"`
}

type Decision struct {
	TS          time.Time          `json:"ts"`
	Kind        string             `json:"kind"`         // "decision"
	ID          string             `json:"id,omitempty"` // keys the routing state kept in States
	SessionID   string             `json:"session_id"`
	Scope       string             `json:"scope"`
	Trigger     string             `json:"trigger"`
	Cause       string             `json:"cause,omitempty"`
	AgentType   string             `json:"agent_type,omitempty"`
	StateTokens int                `json:"state_tokens"`
	Probs       map[string]float64 `json:"probs,omitempty"`
	Confidence  float64            `json:"confidence"`
	JevChoice   string             `json:"jev_choice,omitempty"`
	Chosen      string             `json:"chosen"`
	Model       string             `json:"model"`
	Effort      string             `json:"effort,omitempty"`
	JevModel    string             `json:"jev_model,omitempty"`
	JevCostUSD  float64            `json:"jev_cost_usd"`
	LatencyMS   int64              `json:"latency_ms"`
	Error       string             `json:"error,omitempty"`
	Shadow      *Shadow            `json:"shadow,omitempty"`

	// v2: modes, warm turns and the cost-aware policy.
	Mode       string             `json:"mode,omitempty"`
	ModeP      map[string]float64 `json:"mode_p,omitempty"`
	Warm       bool               `json:"warm,omitempty"`
	From       string             `json:"from,omitempty"` // tier in force before a warm decision
	Kept       bool               `json:"kept,omitempty"` // warm: stayed on From
	KeepReason string             `json:"keep_reason,omitempty"`
	ContinuesP *float64           `json:"continues_p,omitempty"`
	InformsP   *float64           `json:"informs_p,omitempty"` // warm main: the prompt only informs the work in progress
	AskedP     map[string]float64 `json:"asked_p,omitempty"`   // asked tiers (e.g. haiku): Jev's yes-probability
	OfferP     *float64           `json:"offer_p,omitempty"`   // a go-ahead after a detour: the assistant offered more of it (else back to the paused work)
	Label      string             `json:"label,omitempty"`     // workflow stage: its label or phase option
	Loss       map[string]float64 `json:"loss,omitempty"`
	GainUSD    float64            `json:"gain_usd,omitempty"`
	SwitchUSD  float64            `json:"switch_cost_usd,omitempty"`
	Skipped    bool               `json:"skipped,omitempty"`    // warm: Jev not asked (no switch could pay back)
	Signals    map[string]any     `json:"signals,omitempty"`    // user signals (interrupted turn, asked for more thinking)
	Repo       string             `json:"repo,omitempty"`       // repository root, when known
	BudgetCap  string             `json:"budget_cap,omitempty"` // tier lowered to the budget cap

	// v3: the work in progress. ContinuesP and InformsP above are only in
	// older records (the relation question replaced both).
	Relation   map[string]float64 `json:"relation,omitempty"`    // the prompt's relation to the work in progress: Jev's probabilities
	Explicit   map[string]float64 `json:"explicit,omitempty"`    // requests found in the prompt's words (effort_xhigh, mode_off...): Jev's yes-probability
	WorkTier   string             `json:"work_tier,omitempty"`   // the work in progress's tier when deciding
	WorkDone   bool               `json:"work_done,omitempty"`   // a wrap-up had closed it
	PausedTier string             `json:"paused_tier,omitempty"` // the paused work's tier, when a detour paused some
	Work       string             `json:"work,omitempty"`        // what the decision made of it: new, set, raised, resumed, done, reopened, detour-done
	Pauses     bool               `json:"pauses,omitempty"`      // new work below the work in progress, which it paused (resumed: the detour waits)
	Hold       string             `json:"hold,omitempty"`        // what set the tier besides the pick (the work in progress, a request)
}

// AskedMoreThinking reports a prompt that asked for more thinking: the
// user signal of older records, or what raised the tier since (Hold).
func (d Decision) AskedMoreThinking() bool {
	return d.Signals["asks_more_thinking"] == true || strings.Contains(d.Hold, "more thinking") || strings.Contains(d.Hold, "ultrathink")
}

type Usage struct {
	TS                       time.Time `json:"ts"`
	Kind                     string    `json:"kind"` // "usage"
	SessionID                string    `json:"session_id"`
	Scope                    string    `json:"scope"`
	AgentID                  string    `json:"agent_id,omitempty"`
	Routed                   bool      `json:"routed"`
	Tier                     string    `json:"tier,omitempty"`
	Model                    string    `json:"model"`
	Effort                   string    `json:"effort,omitempty"`
	Status                   int       `json:"status"`
	InputTokens              int       `json:"input_tokens"`
	OutputTokens             int       `json:"output_tokens"`
	CacheReadInputTokens     int       `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int       `json:"cache_creation_input_tokens"`
	// Limits is the subscription's usage after this response, per window
	// (anthropic-ratelimit-unified-<window>-utilization, 0-1: "5h", "7d",
	// and any per-model window the API sends), to measure what each model
	// and effort costs in quota.
	Limits map[string]float64 `json:"limits,omitempty"`
}

type Ledger struct{ Path string }

// Append writes one JSON line under an exclusive lock.
func (l Ledger) Append(rec any) error {
	if l.Path == "" {
		return nil
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	unlock, err := flock.Lock(f)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = f.Write(append(line, '\n'))
	return err
}
