// Package statusline renders the routing state for Claude Code's status
// line, optionally after the output of the user's own statusline command.
package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
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

// Run reads the statusline JSON on stdin and prints the chained statusline
// followed by the automodel segment on its own line: chained statuslines are
// often multi-line or padded to the terminal width, which would truncate it.
func Run(env *router.Env, stdin io.Reader, stdout io.Writer) error {
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	var in Input
	_ = json.Unmarshal(raw, &in)

	var parts []string
	if cmd := env.Cfg.StatuslineCommand; cmd != "" {
		if out := chain(env.Cfg.StateDir, cmd, in.SessionID, raw); out != "" {
			parts = append(parts, out)
		}
	}
	if seg := segment(env, in); seg != "" {
		parts = append(parts, seg)
	}
	_, err = fmt.Fprintln(stdout, strings.Join(parts, "\n"))
	return err
}

func segment(env *router.Env, in Input) string {
	if in.SessionID == "" {
		return ""
	}
	isJev := env.IsCustom(in.Model.ID) || env.IsCustom(in.Model.DisplayName)
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return ""
	}
	// The statusline sees model switches first: record them for the hooks.
	if in.Model.ID != "" && sess.Model != in.Model.ID && (isJev || env.IsCustom(sess.Model)) {
		env.State.Update(in.SessionID, func(s *state.Session) bool {
			s.Model, s.ModelSource = in.Model.ID, "statusline"
			return true
		})
	}
	if !isJev {
		return ""
	}
	return Render(env, sess, env.Now())
}

// Render formats "jev → opus-5.5·xhigh 0.82", "jev → opus-5.5·xhigh +ultracode
// 0.74", with a "↻ compact"/"↻ cold"/"↻ switched" flash after a redecision.
func Render(env *router.Env, sess *state.Session, now time.Time) string {
	id := env.Cfg.CustomModelID
	d := sess.Main
	if d == nil {
		d = env.DefaultDecision("main", "default")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s → ", id)
	label := d.Model
	if m := env.Catalog.Model(d.Model); m != nil {
		label = m.ShortLabel()
	}
	b.WriteString(label)
	if d.Effort != "" {
		b.WriteString("·" + d.Effort)
	}
	if d.Mode != "" {
		b.WriteString(" +" + d.Mode)
	}
	switch {
	case sess.Pin != "":
		b.WriteString(" (pinned)")
	case d.Trigger == "default":
		b.WriteString(" (default)")
	case d.Trigger == "fallback":
		b.WriteString(" ⚠ fallback")
	default:
		fmt.Fprintf(&b, " %.2f", d.Confidence)
	}
	if env.OverBudget(sess) {
		b.WriteString(" ⚠ budget")
	}
	if sess.JevIssue != "" {
		b.WriteString(" ⚠ jev: " + sess.JevIssue)
	}
	cause := d.Trigger
	if cause == "fallback" {
		cause = d.Cause
	}
	if (cause == "compact" || cause == "cold" || cause == "warm") && now.Sub(d.DecidedAt) < env.Cfg.StatuslineFlash.Duration {
		if cause == "warm" {
			cause = "switched"
		}
		b.WriteString(" ↻ " + cause)
	}
	return b.String()
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
			tmp := out + ".tmp"
			script := fmt.Sprintf("(%s) < %q > %q 2>/dev/null; mv -f %q %q; rm -f %q", command, in, tmp, tmp, out, running)
			cmd := exec.Command("sh", "-c", script)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			if cmd.Start() == nil {
				cmd.Process.Release()
			}
		}
	}
	for deadline := time.Now().Add(1500 * time.Millisecond); ; time.Sleep(20 * time.Millisecond) {
		if b, err := os.ReadFile(out); err == nil {
			return strings.TrimRight(string(b), "\n")
		}
		if time.Now().After(deadline) {
			return ""
		}
	}
}

var safeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)
