package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// User slash commands: /why shows the session's latest decisions and /flag
// labels a wrong one, without leaving Claude Code. They live next to
// settings.json (~/.claude/commands). Claude Code substitutes
// ${CLAUDE_SESSION_ID} and runs the !`...` line before the prompt is sent.

const commandMarker = "<!-- automodel -->"

func (o Options) commandsDir() string { return filepath.Join(filepath.Dir(o.SettingsPath), "commands") }

func (o Options) commands() map[string]string {
	base := fmt.Sprintf("%s --config %s", o.Exe, o.ConfigPath)
	cmd := func(desc, hint, sub, args string) string {
		h := ""
		if hint != "" {
			h = "argument-hint: " + hint + "\n"
		}
		return fmt.Sprintf(`---
description: %s
%sallowed-tools: Bash(%s %s *)
disable-model-invocation: true
---
%s
!`+"`%s %s --session \"${CLAUDE_SESSION_ID}\" %s 2>&1 || true`"+`

Show the output above to the user as is, in a code block, with no commentary.
`, desc, h, base, sub, commandMarker, base, sub, args)
	}
	return map[string]string{
		"why.md":  cmd("automodel: the latest routing decisions of this session", "", "why", "-n 3"),
		"flag.md": cmd("automodel: the last pick was wrong (e.g. /flag xhigh too hard for low)", "<tier> [why]", "flag", `-- "$ARGUMENTS"`),
	}
}

// installCommands writes the commands, leaving files the user wrote alone.
func installCommands(o Options) error {
	dir := o.commandsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, body := range o.commands() {
		p := filepath.Join(dir, name)
		if b, err := os.ReadFile(p); err == nil && !strings.Contains(string(b), commandMarker) {
			o.Log("%s kept (not written by automodel)", p)
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return err
		}
	}
	o.Log("slash commands: /why, /flag (%s)", dir)
	return nil
}

// removeCommands deletes the commands automodel wrote.
func removeCommands(o Options) {
	for name := range o.commands() {
		p := filepath.Join(o.commandsDir(), name)
		if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), commandMarker) {
			os.Remove(p)
		}
	}
}
