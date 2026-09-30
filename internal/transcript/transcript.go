// Package transcript reads what the router needs from a Claude Code session
// transcript (JSONL): the compaction summary, recent user prompts, the
// selected model and whether a turn is still running. Only the tail of the
// file is read.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
)

// TailBytes bounds how much of a transcript is scanned.
const TailBytes = 8 << 20

type entry struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	IsMeta           bool            `json:"isMeta"`
	IsSidechain      bool            `json:"isSidechain"`
	Message          json.RawMessage `json:"message"`
	Attachment       *struct {
		Type     string `json:"type"`
		Identity *struct {
			ModelID string `json:"modelId"`
		} `json:"identity"`
		// queued_command: a prompt typed while Claude was working.
		Prompt      json.RawMessage `json:"prompt"`
		CommandMode string          `json:"commandMode"`
	} `json:"attachment"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
}

// Info is what one tail scan extracts.
type Info struct {
	CompactSummary string   // last compaction summary, if any
	UserPrompts    []string // real user prompts, oldest first (prompts typed mid-turn included)
	Model          string   // last model announced to the main thread
	LastAssistant  string   // text of the last assistant message
	// Interrupted is set when the user stopped a turn (Esc), and
	// InterruptedAt is how many prompts came before that interruption.
	Interrupted   bool
	InterruptedAt int
	// MidTurn: Claude was still working when the file was read (a user
	// prompt started a turn, or its last message called a tool, and no turn
	// end followed), so a prompt read now was typed during the turn.
	// TurnPrompt is the prompt that started that turn: when it is the one
	// being decided (a late decision reads the file after it was written),
	// nothing was typed mid-turn.
	MidTurn    bool
	TurnPrompt string
}

// Read scans the tail of the transcript.
func Read(path string) (Info, error) {
	var info Info
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return info, err
	}
	var r io.Reader = f
	partial := false
	if st.Size() > TailBytes {
		if _, err := f.Seek(st.Size()-TailBytes, io.SeekStart); err != nil {
			return info, err
		}
		partial = true
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), TailBytes)
	for sc.Scan() {
		line := sc.Bytes()
		if partial {
			partial = false // first line is cut
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e entry
		if json.Unmarshal(line, &e) != nil || e.IsSidechain {
			continue
		}
		switch e.Type {
		case "attachment":
			a := e.Attachment
			switch {
			case a == nil:
			case a.Type == "model" && a.Identity != nil && a.Identity.ModelID != "":
				info.Model = a.Identity.ModelID
			case a.Type == "queued_command" && a.CommandMode == "prompt":
				// Typed while Claude worked: it never becomes a user line.
				var text string
				if json.Unmarshal(a.Prompt, &text) == nil {
					if text = strings.TrimSpace(text); text != "" && !IsSynthetic(text) && !IsPeer(text) {
						info.UserPrompts = append(info.UserPrompts, text)
					}
				}
			}
		case "system":
			switch e.Subtype {
			case "compact_boundary":
				info.CompactSummary = ""
				info.UserPrompts = nil
				info.LastAssistant = ""
				info.Interrupted, info.InterruptedAt = false, 0
				info.MidTurn = false
			case "turn_duration", "stop_hook_summary":
				info.MidTurn = false // the turn ended
			}
		case "assistant":
			if t := assistantText(e.Message); t != "" {
				info.LastAssistant = t
			}
			info.MidTurn = callsTool(e.Message)
		case "user":
			text := userText(e.Message)
			switch {
			case e.IsCompactSummary:
				info.CompactSummary = text
			case strings.HasPrefix(text, "[Request interrupted by user"):
				info.Interrupted, info.InterruptedAt = true, len(info.UserPrompts)
				info.MidTurn = false
			case e.IsMeta || text == "" || IsSynthetic(text) || IsPeer(text):
			default:
				// A prompt starts a turn: Claude Code writes the turn's own
				// entries in batches, seconds later, so a prompt typed early
				// in the turn may see no assistant entry after it yet.
				info.UserPrompts = append(info.UserPrompts, text)
				info.MidTurn, info.TurnPrompt = true, text
			}
		}
	}
	return info, sc.Err()
}

// callsTool reports whether an assistant message stopped to call a tool:
// the turn goes on with the result.
func callsTool(raw json.RawMessage) bool {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	if m.StopReason != "" {
		return m.StopReason == "tool_use"
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	json.Unmarshal(m.Content, &blocks)
	for _, b := range blocks {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

// assistantText returns the text blocks of an assistant message.
func assistantText(raw json.RawMessage) string {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// userText returns the text of a user message, ignoring tool results.
func userText(raw json.RawMessage) string {
	var m message
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && !strings.HasPrefix(b.Text, "<system-reminder>") {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// IsSynthetic reports prompts injected by Claude Code rather than typed by
// the user: subagent hand-backs, slash-command wrappers, task notifications,
// and the entries of a shell command run with "!" (its input and output:
// no turn starts, and the next prompt is not typed mid-turn).
func IsSynthetic(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{"<agent-message", "<command-", "<local-command", "<task-notification", "<system-reminder", "[SYSTEM NOTIFICATION",
		"<bash-input", "<bash-stdout", "<bash-stderr"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// IsPeer reports a message another Claude session sent to this one. It
// asks for something, so it is routed, but it is not the user's prompt:
// its words are no request of the user's, and it never lowers the effort
// of the work in progress. Only the start counts: a prompt may quote the
// phrase.
func IsPeer(text string) bool {
	t := strings.TrimSpace(text)
	return strings.HasPrefix(t, "<cross-session-message") || strings.HasPrefix(t, "Another Claude session sent a message")
}

// MidTurnFor reports whether prompt was typed while Claude worked: the
// transcript shows a turn still running, and that turn isn't the prompt's
// own (read after the prompt was written).
func (info *Info) MidTurnFor(prompt string) bool {
	return info != nil && info.MidTurn && strings.Join(strings.Fields(info.TurnPrompt), " ") != strings.Join(strings.Fields(prompt), " ")
}
