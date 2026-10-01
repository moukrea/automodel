// Package statusline renders the routing state for Claude Code's status
// line, optionally after the output of the user's own statusline command,
// or as JSON for status lines that render it themselves (agentline).
package statusline

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

type Input struct {
	SessionID string `json:"session_id"`
	Model     struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
}

// ChainedEnv is set to 1 in the environment of the chained statusline
// command: a status line that could call `automodel statusline --json`
// itself (agentline) must not, since the segment is printed after it.
const ChainedEnv = "AUTOMODEL_CHAINED"

// chainedEnv is the chained command's environment: ours, with ChainedEnv=1
// replacing any value it had (names are case-insensitive on Windows).
func chainedEnv() []string {
	env := []string{ChainedEnv + "=1"}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.EqualFold(k, ChainedEnv) {
			env = append(env, kv)
		}
	}
	return env
}

// Run reads the statusline JSON on stdin and prints the chained statusline
// followed by the automodel segment on its own line: chained statuslines are
// often multi-line or padded to the terminal width, which would truncate it.
func Run(env *router.Env, stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	in := parse(raw)
	var parts []string
	if cmd := env.Cfg.StatuslineCommand; cmd != "" {
		if out := chain(env.Cfg.StateDir, cmd, in.SessionID, raw); out != "" {
			parts = append(parts, out)
		}
	}
	if sess := observe(env, in, true); sess != nil {
		parts = append(parts, Render(env, sess, env.Now()))
	}
	_, err = fmt.Fprintln(stdout, strings.Join(parts, "\n"))
	return err
}

// RunJSON is `statusline --json`: the same side effect as Run (model switches
// recorded), never the chained command, and one line of JSON (View). With
// readOnly (`--read-only`, for tools other than Claude Code's statusline) it
// writes nothing: no model recorded, no state file for an unknown session.
func RunJSON(env *router.Env, stdin io.Reader, stdout io.Writer, readOnly bool) error {
	raw, _ := io.ReadAll(stdin)
	v := View{V: 1}
	if sess := observe(env, parse(raw), !readOnly); sess != nil {
		v = Build(env, sess, env.Now())
	}
	return writeJSON(stdout, v)
}

// CatalogErrorJSON is what `statusline --json` prints when the catalog can't
// be loaded (the text statusline prints "automodel: ⚠ catalog").
func CatalogErrorJSON(alias string, stdin io.Reader, stdout io.Writer) error {
	raw, _ := io.ReadAll(stdin)
	in := parse(raw)
	custom := func(m string) bool { return strings.TrimSuffix(m, "[1m]") == alias }
	v := View{V: 1, Routed: in.SessionID != "" && (custom(in.Model.ID) || custom(in.Model.DisplayName)),
		Alias: alias, State: "error", Issue: "catalog", Text: "automodel: ⚠ catalog", full: true}
	return writeJSON(stdout, v)
}

// NotRoutedJSON is the JSON for a session automodel doesn't route.
func NotRoutedJSON(stdout io.Writer) error { return writeJSON(stdout, View{V: 1}) }

func parse(raw []byte) Input {
	var in Input
	_ = json.Unmarshal(raw, &in)
	return in
}

// observe records model switches (the statusline sees them first) when
// record is set, and returns the session when it is routed, nil otherwise.
func observe(env *router.Env, in Input, record bool) *state.Session {
	if in.SessionID == "" {
		return nil
	}
	isJev := env.IsCustom(in.Model.ID) || env.IsCustom(in.Model.DisplayName)
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil
	}
	if record && in.Model.ID != "" && sess.Model != in.Model.ID && (isJev || env.IsCustom(sess.Model)) {
		env.State.Update(in.SessionID, func(s *state.Session) bool {
			s.Model, s.ModelSource = in.Model.ID, "statusline"
			return true
		})
	}
	if !isJev {
		return nil
	}
	return sess
}

// View is the routing state of a session, as `statusline --json` prints it
// (schema v1). A session that isn't routed is {"v":1,"routed":false}, or
// every field with state "error" when the catalog can't load (CatalogErrorJSON).
type View struct {
	V          int     `json:"v"`
	Routed     bool    `json:"routed"`
	Alias      string  `json:"alias"`      // the custom model ("jev")
	Model      string  `json:"model"`      // catalog model key
	Label      string  `json:"label"`      // catalog label ("Opus 5.5")
	Effort     string  `json:"effort"`     // "" when none
	Mode       string  `json:"mode"`       // "ultracode", "" when none
	State      string  `json:"state"`      // routed|default|fallback|pinned|error
	Confidence float64 `json:"confidence"` // 0 unless routed
	Pin        string  `json:"pin"`        // the pinned effort
	Issue      string  `json:"issue"`      // why Jev couldn't be asked, or the error
	Flash      string  `json:"flash"`      // switched|compact|cold after a recent redecision
	Budget     string  `json:"budget"`     // "over" when the session is over its spending cap
	Why        string  `json:"why"`        // the last decision's short reason (aside, asked, go-ahead...)
	From       string  `json:"from"`       // the effort the last decision left, when it changed it
	// ClaudeEffort is the effort Claude Code itself shows (its spinner says
	// "thinking with X effort"): its own setting, not the routed effort.
	// "" when it matches Effort or is unknown.
	ClaudeEffort string `json:"claude_effort"`
	Text         string `json:"text"` // the text segment

	full  bool // print every field even when not routed
	short string
}

func writeJSON(w io.Writer, v View) error {
	var err error
	if v.Routed || v.full {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		err = enc.Encode(v) // one line
	} else {
		_, err = fmt.Fprintf(w, "{\"v\":%d,\"routed\":false}\n", v.V)
	}
	return err
}

// Build computes the routing state of a routed session.
func Build(env *router.Env, sess *state.Session, now time.Time) View {
	d := sess.Main
	if d == nil {
		d = env.DefaultDecision("main", "default")
	}
	v := View{V: 1, Routed: true, Alias: env.Cfg.CustomModelID, Model: d.Model, Label: d.Model, short: d.Model,
		Effort: d.Effort, Mode: d.Mode, Pin: sess.Pin, Issue: sess.JevIssue, Why: d.Why, From: d.From}
	if m := env.Catalog.Model(d.Model); m != nil {
		v.short = m.ShortLabel()
		if m.Label != "" {
			v.Label = m.Label
		}
	}
	switch {
	case sess.Pin != "":
		v.State = "pinned"
	case d.Trigger == "default":
		v.State = "default"
	case d.Trigger == "fallback":
		v.State = "fallback"
	default:
		v.State, v.Confidence = "routed", d.Confidence
	}
	if env.OverBudget(sess) {
		v.Budget = "over"
	}
	if ce := cmp.Or(sess.ClientEffortLast, sess.ClientEffort0); ce != "" && d.Effort != "" && ce != d.Effort {
		v.ClaudeEffort = ce
	}
	cause := d.Trigger
	if cause == "fallback" {
		cause = d.Cause
	}
	if (cause == "compact" || cause == "cold" || cause == "warm") && now.Sub(d.DecidedAt) < env.Cfg.StatuslineFlash.Duration {
		if cause == "warm" {
			cause = "switched"
		}
		v.Flash = cause
	}
	v.Text = v.text()
	return v
}

// text formats "automodel: Opus 5.5 high (xhigh→high · aside 0.59)": the
// model and effort in force, then the last decision in short (the effort it
// left, why, Jev's confidence). The effort Claude Code still shows, when it
// differs, follows struck through ("x̶h̶i̶g̶h̶"); a "↻ compact"/"↻ cold"/
// "↻ switched" flash follows a redecision.
func (v View) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "automodel: %s", cmp.Or(v.Label, v.short))
	if v.Effort != "" {
		b.WriteString(" " + v.Effort)
	}
	if v.Mode != "" {
		b.WriteString(" +" + v.Mode)
	}
	if v.ClaudeEffort != "" {
		b.WriteString(" " + strike(v.ClaudeEffort))
	}
	var why []string
	if v.From != "" && v.Effort != "" {
		why = append(why, v.From+"→"+v.Effort)
	}
	switch v.State {
	case "pinned":
		why = append(why, "pinned")
	case "default":
		why = append(why, "default")
	case "fallback":
		why = append(why, "⚠ fallback")
	default:
		if v.Why != "" && v.Confidence > 0 {
			why = append(why, fmt.Sprintf("%s %.2f", v.Why, v.Confidence))
		} else if v.Why != "" {
			why = append(why, v.Why)
		} else if v.Confidence > 0 {
			why = append(why, fmt.Sprintf("%.2f", v.Confidence))
		}
	}
	if len(why) > 0 {
		b.WriteString(" (" + strings.Join(why, " · ") + ")")
	}
	if v.Budget != "" {
		b.WriteString(" ⚠ budget")
	}
	if v.Issue != "" {
		b.WriteString(" ⚠ jev: " + v.Issue)
	}
	if v.Flash != "" {
		b.WriteString(" ↻ " + v.Flash)
	}
	return b.String()
}

// strike draws s struck through with combining characters, which survive
// any status line that embeds the text (no escape codes).
func strike(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(r)
		b.WriteRune('\u0336')
	}
	return b.String()
}

// Render formats the text segment of a routed session.
// In Claude Code's own status line the effort it still shows is red.
func Render(env *router.Env, sess *state.Session, now time.Time) string {
	v := Build(env, sess, now)
	if v.ClaudeEffort == "" {
		return v.Text
	}
	st := strike(v.ClaudeEffort)
	return strings.Replace(v.Text, st, "\x1b[31m"+st+"\x1b[0m", 1)
}

// chain returns the user's own statusline. Claude Code cancels a run when
// the next update arrives, and updates keep coming while a turn streams: a
// chained script taking a few hundred milliseconds would keep the whole
// statusline, automodel segment included, from showing for tens of seconds.
// So the chained script runs detached and writes a cache; each run prints
// the cache (at most one refresh old) and only a first run waits for it.
func chain(stateDir, command, sessionID string, stdin []byte) string {
	dir := filepath.Join(stateDir, "statusline")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	id := safeName.ReplaceAllString(sessionID, "_")
	if id == "" {
		id = "default"
	}
	in, out, running := filepath.Join(dir, id+".in"), filepath.Join(dir, id+".out"), filepath.Join(dir, id+".running")
	if st, err := os.Stat(running); err != nil || time.Since(st.ModTime()) > 3*time.Second {
		if os.WriteFile(in, stdin, 0o600) == nil && os.WriteFile(running, nil, 0o600) == nil {
			startChain(command, in, out+".tmp", out, running)
		}
	}
	for deadline := time.Now().Add(1500 * time.Millisecond); ; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(out); err == nil {
			return strings.TrimRight(string(b), "\r\n")
		}
		if time.Now().After(deadline) {
			return ""
		}
	}
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)
