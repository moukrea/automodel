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
	// Why is the short reason of the decision for the status line: the
	// prompt's relation to the work (continue, aside...), "asked", "new",
	// "go-ahead", "mid-turn", "peer".
	Why string `json:"why,omitempty"`
	// WhyP is Jev's probability of that relation (0 when Why is no relation).
	WhyP float64 `json:"why_p,omitempty"`
	// From is the effort before this decision, when it changed it.
	From string `json:"from,omitempty"`
	DecidedAt  time.Time          `json:"decided_at"`
	Epoch      int                `json:"epoch"`
}

// PinnedTier is the tier of a decision pinned to a model outside the
// catalog's tiers ([model:X]): the proxy applies its model and effort as is.
const PinnedTier = "pinned"

// Work is the work in progress of the main session: the tier and mode it
// needs and the prompt that started it. It is set by the first decision, a
// prompt that starts separate work, or an effort, a mode or a model asked
// in words; raised when a follow-up needs more; kept across side
// questions, asides, wrap-ups, pins and compactions. Model is another
// model than the tiers' the user asked for in words: the work runs on it
// (the tier is then only its level) until separate new work starts. Done:
// a wrap-up closed it; only more work on it (a go-ahead, an addition)
// reopens it and holds its level. Kept (paused work only): a detour a
// go-ahead went back from by default, below the work it went back to,
// kept in case the assistant had offered more of it.
type Work struct {
	Tier  string    `json:"tier"`
	Mode  string    `json:"mode,omitempty"`
	Model string    `json:"model,omitempty"`
	Goal  string    `json:"goal,omitempty"` // head of the prompt that started it
	Since time.Time `json:"since,omitzero"` // started; for paused work, paused
	Done  bool      `json:"done,omitempty"`
	Kept  bool      `json:"kept,omitempty"`
}

// WorkGoalChars bounds the goal kept for the work in progress.
const WorkGoalChars = 400

// PausedTTL is how long paused work waits to be resumed.
const PausedTTL = 2 * time.Hour

// PausedWork is the work a detour paused (a new task below it), while it
// can still be resumed (nil: none, or paused more than PausedTTL ago).
func (s *Session) PausedWork(now time.Time) *Work {
	if s.Paused == nil || now.Sub(s.Paused.Since) > PausedTTL {
		return nil
	}
	return s.Paused
}

// WorkInProgress is the session's work in progress; a session from before
// it was recorded takes its decision in force (nil: none, or a decision
// the user pinned: that level was their call, not the work's).
func (s *Session) WorkInProgress() *Work {
	if s.Work != nil {
		return s.Work
	}
	if s.Main == nil || s.Main.Tier == PinnedTier || s.Main.Tier == "" || s.Main.Trigger == "pinned" {
		return nil
	}
	return &Work{Tier: s.Main.Tier, Mode: s.Main.Mode}
}

// Asked is an effort or a model (catalog key) the user asked for in words,
// for the work that started at WorkSince (Work.Since).
type Asked struct {
	Effort    string    `json:"effort,omitempty"`
	Model     string    `json:"model,omitempty"`
	WorkSince time.Time `json:"work_since,omitzero"`
}

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
	// Work is the work in progress: follow-ups of it (go-aheads, additions,
	// side questions, mid-turn remarks) keep at least its tier and mode.
	// Paused is the work a new task below it set aside (a detour while it
	// was pending): a prompt that goes back to it resumes it.
	Work   *Work `json:"work,omitempty"`
	Paused *Work `json:"paused,omitempty"`

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

	// PendingAsked is the effort or the model the user asked for in words
	// that a late decision applied (Jev answered after the hook's timeout,
	// when the hook could no longer tell Claude): the next prompt's hook
	// says so if the decision in force still runs it.
	PendingAsked *Asked `json:"pending_asked,omitempty"`

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
	// ClientUltracode: Claude Code's own effort is ultracode (its payload
	// says xhigh), from the transcript at the last prompt.
	ClientUltracode bool `json:"client_ultracode,omitempty"`

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
