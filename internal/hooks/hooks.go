// Package hooks implements the Claude Code hook subcommands. Every hook reads
// the event JSON on stdin, may print one JSON object on stdout, and never
// fails the user's action: errors are logged and the hook exits 0.
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/transcript"
)

// Input is the union of the hook event fields we use.
type Input struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	AgentID        string          `json:"agent_id"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Trigger        string          `json:"trigger"` // PreCompact: manual|auto
	Source         string          `json:"source"`  // SessionStart / PostModelSwitch
	Model          string          `json:"model"`   // SessionStart (sometimes)
	ToModel        string          `json:"to_model"`
	CacheExpired   *bool           `json:"prompt_cache_likely_expired"`
	CacheWarm      *bool           `json:"prompt_cache_warm"`
	ContextTokens  int             `json:"context_tokens"`
	// LastAssistant is the assistant message the prompt's own hook read,
	// handed to its late decision (not from Claude Code).
	LastAssistant *string `json:"automodel_last_assistant,omitempty"`
}

type Output struct {
	HookSpecificOutput *Specific `json:"hookSpecificOutput,omitempty"`
	// Decision "block" stops a prompt (UserPromptSubmit), with Reason
	// shown to the user; SystemMessage is a notice (SessionStart).
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

type Specific struct {
	HookEventName            string         `json:"hookEventName"`
	AdditionalContext        string         `json:"additionalContext,omitempty"`
	PermissionDecision       string         `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
}

type handler func(context.Context, *router.Env, *Input) (*Output, error)

var handlers = map[string]handler{
	"decide":        Decide,
	"agent":         Agent,
	"workflow":      Workflow,
	"precompact":    PreCompact,
	"session-start": SessionStart,
	"model-switch":  ModelSwitch,
}

func Names() []string {
	return []string{"decide", "agent", "workflow", "precompact", "session-start", "model-switch"}
}

// Run executes a hook by name. It returns an error only for usage mistakes.
func Run(name string, env *router.Env, stdin io.Reader, stdout io.Writer) error {
	h, ok := handlers[name]
	if !ok {
		return fmt.Errorf("unknown hook %q (want one of %v)", name, Names())
	}
	var in Input
	if path := os.Getenv(InputFileEnv); path != "" {
		if f, err := os.Open(path); err == nil {
			stdin = f
			defer func() { f.Close(); os.Remove(path) }()
		}
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		log.Printf("hook %s: bad input: %v", name, err)
		return nil
	}
	if in.SessionID == "" {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("hook %s: panic: %v", name, r)
		}
	}()
	switch name {
	case "decide", "session-start":
		if msg := proxyProblem(context.Background(), env); msg != "" {
			log.Printf("hook %s: %s", name, msg)
			out := &Output{SystemMessage: msg}
			if name == "decide" {
				out = &Output{Decision: "block", Reason: msg}
			}
			return json.NewEncoder(stdout).Encode(out)
		}
	}
	out, err := h(context.Background(), env, &in)
	if err != nil {
		log.Printf("hook %s (%s): %v", name, in.SessionID, err)
		return nil
	}
	if out != nil {
		return json.NewEncoder(stdout).Encode(out)
	}
	return nil
}

// Model detection. Hook inputs don't carry the model (except SessionStart,
// sometimes), so we combine what the proxy, statusline and switch hooks
// recorded, then the transcript, then settings files.

type modelInfo struct {
	Model  string
	Source string
}

func detectModel(env *router.Env, sess *state.Session, in *Input, tr *transcript.Info) modelInfo {
	if sess.Model != "" {
		return modelInfo{sess.Model, sess.ModelSource}
	}
	if tr != nil && tr.Model != "" {
		return modelInfo{tr.Model, "transcript"}
	}
	if m := settingsModel(in.Cwd); m != "" {
		return modelInfo{m, "settings"}
	}
	return modelInfo{}
}

// settingsModel reads the model Claude Code was started with, highest
// precedence first: the --model flag of the parent claude process
// ($CLAUDE_PID), ANTHROPIC_MODEL, then the settings files.
func settingsModel(cwd string) string {
	if m := cliModel(os.Getenv("CLAUDE_PID")); m != "" {
		return m
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return m
	}
	project := os.Getenv("CLAUDE_PROJECT_DIR")
	if project == "" {
		project = cwd
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(project, ".claude", "settings.local.json"),
		filepath.Join(project, ".claude", "settings.json"),
		filepath.Join(home, ".claude", "settings.json"),
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(b, &s) == nil && s.Model != "" {
			return s.Model
		}
	}
	return ""
}

// transcriptIfUnknown reads the transcript when the session model isn't
// recorded yet: its model identity is more reliable than the settings files.
func transcriptIfUnknown(sess *state.Session, in *Input) *transcript.Info {
	if sess.Model != "" {
		return nil
	}
	return readTranscript(in.TranscriptPath)
}

func readTranscript(path string) *transcript.Info {
	if path == "" {
		return nil
	}
	info, err := transcript.Read(path)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("transcript %s: %v", path, err)
	}
	return &info
}

// cliModel extracts --model from a process command line (Linux /proc, else ps).
func cliModel(pid string) string {
	if pid == "" {
		return ""
	}
	var args []string
	if b, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline")); err == nil {
		args = strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	} else if out, err := exec.Command("ps", "-o", "args=", "-p", pid).Output(); err == nil {
		args = strings.Fields(string(out))
	}
	return modelFlag(args)
}

func modelFlag(args []string) string {
	for i, a := range args {
		if a == "--model" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--model="); ok {
			return v
		}
	}
	return ""
}
