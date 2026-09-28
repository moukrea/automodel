// Package state is the per-session state shared by hooks, the proxy and the
// statusline: one JSON file per session_id, updated under an flock.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/flock"
)

// Decision is the tier applied to a scope (main session or one subagent).
type Decision struct {
	Scope      string             `json:"scope"`
	Tier       string             `json:"tier"`
	Model      string             `json:"model"`  // catalog model key
	APIID      string             `json:"api_id"` // resolved API model ID
	Effort     string             `json:"effort,omitempty"`
	Workflows  bool               `json:"workflows,omitempty"`
	Mode       string             `json:"mode,omitempty"` // catalog mode layered on the tier (ultracode)
	Trigger    string             `json:"trigger"`        // initial|compact|cold|fallback|default
	Cause      string             `json:"cause,omitempty"`
	JevChoice  string             `json:"jev_choice,omitempty"`
	Confidence float64            `json:"confidence,omitempty"`
	Probs      map[string]float64 `json:"probs,omitempty"`
	DecidedAt  time.Time          `json:"decided_at"`
	Epoch      int                `json:"epoch"`
}

// PinnedTier is the tier of a decision pinned to a model outside the
// catalog's tiers ([model:X]): the proxy applies its model and effort as is.
const PinnedTier = "pinned"

// PendingAgent is registered by the agent hook and bound by the proxy to the
// X-Claude-Code-Agent-Id of the subagent whose first message contains Prompt.
type PendingAgent struct {
	Prompt    string    `json:"prompt"` // normalized head of the Agent prompt
	Decision  Decision  `json:"decision"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	SessionID string `json:"session_id"`

	// Model is the model selected in Claude Code for the main thread, and
	// where that knowledge came from (proxy|statusline|session_start|
	// model_switch|transcript|settings).
	Model       string `json:"model,omitempty"`
	ModelSource string `json:"model_source,omitempty"`

	Main *Decision `json:"main,omitempty"`

	LastPromptAt  time.Time `json:"last_prompt_at,omitzero"`
	LastAPIAt     time.Time `json:"last_api_at,omitzero"`
	ContextTokens int       `json:"context_tokens,omitempty"`

	// PeakContextTokens is the largest context seen on the routed model and
	// Compactions how many times the session was compacted: after a compact
	// the context is small again, but the session is known to run long.
	PeakContextTokens int `json:"peak_context_tokens,omitempty"`
	Compactions       int `json:"compactions,omitempty"`

	CompactPending bool   `json:"compact_pending,omitempty"`
	CompactTrigger string `json:"compact_trigger,omitempty"`
	ColdHint       bool   `json:"cold_hint,omitempty"`

	// UltracodeOn is whether the model was last told ultracode is on, and
	// UltracodeEpoch the decision epoch that notice belonged to.
	UltracodeOn    bool `json:"ultracode_on,omitempty"`
	UltracodeEpoch int  `json:"ultracode_epoch,omitempty"`

	Repo *RepoSignals `json:"repo,omitempty"`

	// Per-turn effort, for models whose catalog entry has per_turn_effort:
	// the top-level effort stays EffortBase for the whole conversation, and
	// later changes are effort-only system messages the proxy inserts before
	// the user turn they apply from (EffortMarks), so an effort change keeps
	// the prompt cache. PendingEffort is a change decided by the hook and not
	// yet bound to a message. PerTurnRejected turns the mechanism off after
	// the API refused it, until the next compaction.
	EffortBase      string         `json:"effort_base,omitempty"`
	EffortMarks     []EffortMark   `json:"effort_marks,omitempty"`
	PendingEffort   *PendingEffort `json:"pending_effort,omitempty"`
	PerTurnRejected bool           `json:"per_turn_rejected,omitempty"`

	// SpendUSD and Prompts measure the routed main thread since the last
	// epoch reset; their ratio calibrates switch-cost decisions.
	SpendUSD float64 `json:"spend_usd,omitempty"`
	Prompts  int     `json:"prompts,omitempty"`
	// TotalUSD is the whole session's spend, all scopes (budget cap).
	TotalUSD float64 `json:"total_usd,omitempty"`

	// Pin is an effort the user chose (Claude Code's /effort, or an
	// [effort:X] tag in a prompt): routing stops until it is released
	// (/effort back to ClientEffort0, or [effort:auto]). ClientEffort0 is
	// the effort Claude Code sent first, i.e. its default.
	Pin              string `json:"pin,omitempty"`
	PinModel         string `json:"pin_model,omitempty"`  // [model:X]: catalog model the session is pinned to
	PinSource        string `json:"pin_source,omitempty"` // "/effort" | "prompt"
	ClientEffort0    string `json:"client_effort0,omitempty"`
	ClientEffortLast string `json:"client_effort_last,omitempty"` // last one seen: pins follow changes

	// JevIssue is why the last decision couldn't ask Jev (no key, timeout,
	// HTTP error), cleared by the next successful answer.
	JevIssue   string    `json:"jev_issue,omitempty"`
	JevIssueAt time.Time `json:"jev_issue_at,omitzero"`

	PendingAgents []PendingAgent       `json:"pending_agents,omitempty"`
	Agents        map[string]*Decision `json:"agents,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// EffortMark switches the effort from message Index on. Anchor is a hash of
// that message, so a rewritten history drops the marks instead of misplacing them.
type EffortMark struct {
	Index  int    `json:"index"`
	Anchor string `json:"anchor"`
	Effort string `json:"effort"`
}

type PendingEffort struct {
	Effort    string    `json:"effort"`
	Prompt    string    `json:"prompt"` // normalized head of the prompt it applies from
	CreatedAt time.Time `json:"created_at"`
}

// ResetEffortEpoch starts a new conversation epoch (first prompt,
// compaction, model change): the proxy sets a fresh top-level effort.
func (s *Session) ResetEffortEpoch() {
	s.EffortBase, s.EffortMarks, s.PendingEffort, s.PerTurnRejected = "", nil, nil, false
	s.SpendUSD, s.Prompts = 0, 0
}

type RepoSignals struct {
	Root         string   `json:"root,omitempty"`
	Languages    []string `json:"languages,omitempty"`
	Files        int      `json:"files"`
	DiffStat     string   `json:"diff_stat,omitempty"`
	ClaudeMDHead string   `json:"claude_md_head,omitempty"`
}

// LastActivity is the most recent prompt or API request of the session.
func (s *Session) LastActivity() time.Time {
	if s.LastAPIAt.After(s.LastPromptAt) {
		return s.LastAPIAt
	}
	return s.LastPromptAt
}

type Store struct{ Dir string }

var safeID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

var ErrBadID = errors.New("invalid session id")

func (s Store) sessionPath(id string) string { return filepath.Join(s.Dir, "sessions", id+".json") }

// Load reads a session without locking. A missing session returns an empty one.
func (s Store) Load(id string) (*Session, error) {
	if !safeID.MatchString(id) {
		return nil, ErrBadID
	}
	return s.read(id)
}

func (s Store) read(id string) (*Session, error) {
	data, err := os.ReadFile(s.sessionPath(id))
	// On Windows a read racing the atomic replace of the file (another
	// process's Update) fails with a sharing violation for a moment: a
	// failed read would route the request to the default tier.
	for i := 0; i < 20 && err != nil && !errors.Is(err, os.ErrNotExist); i++ {
		time.Sleep(10 * time.Millisecond)
		data, err = os.ReadFile(s.sessionPath(id))
	}
	if errors.Is(err, os.ErrNotExist) {
		return &Session{SessionID: id}, nil
	}
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		// A corrupt file must not wedge the session: start over.
		return &Session{SessionID: id}, nil
	}
	sess.SessionID = id
	return &sess, nil
}

// Update runs fn on the session under an exclusive lock and writes the result
// atomically. If fn returns false the file is left untouched.
func (s Store) Update(id string, fn func(*Session) bool) (*Session, error) {
	if !safeID.MatchString(id) {
		return nil, ErrBadID
	}
	dir := filepath.Join(s.Dir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	unlock, err := lock(filepath.Join(dir, id+".lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	sess, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if !fn(sess) {
		return sess, nil
	}
	sess.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(sess, "", " ")
	if err != nil {
		return nil, err
	}
	tmp := s.sessionPath(id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	return sess, os.Rename(tmp, s.sessionPath(id))
}

func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	unlock, err := flock.Lock(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlock(); f.Close() }, nil
}

// Prune removes session files and prompt index entries untouched for longer
// than maxAge, and returns the number of sessions removed.
func (s Store) Prune(maxAge time.Duration) int {
	n := 0
	for _, sub := range []string{"sessions", "prompts", "statusline", "states"} {
		dir := filepath.Join(s.Dir, sub)
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || time.Since(info.ModTime()) < maxAge {
				continue
			}
			if os.Remove(filepath.Join(dir, e.Name())) == nil && strings.HasSuffix(e.Name(), ".json") {
				n++
			}
		}
	}
	return n
}

// Prompt index: the fallback that maps a prompt to its session when a
// request carries no session ID (spec §5.1).

func PromptKey(text string) string {
	h := sha256.Sum256([]byte(NormalizePrompt(text)))
	return hex.EncodeToString(h[:16])
}

// NormalizePrompt collapses whitespace so hook and proxy views compare equal.
func NormalizePrompt(text string) string { return strings.Join(strings.Fields(text), " ") }

func (s Store) IndexPrompt(text, sessionID string) error {
	dir := filepath.Join(s.Dir, "prompts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, PromptKey(text)), []byte(sessionID), 0o600)
}

func (s Store) LookupPrompt(text string) string {
	b, err := os.ReadFile(filepath.Join(s.Dir, "prompts", PromptKey(text)))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if !safeID.MatchString(id) {
		return ""
	}
	return id
}
