// Package transcript reads what the router needs from a Claude Code session
// transcript (JSONL): the compaction summary, recent user prompts and the
// selected model. Only the tail of the file is read.
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
	} `json:"attachment"`
}

type message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Info is what one tail scan extracts.
type Info struct {
	CompactSummary string   // last compaction summary, if any
	UserPrompts    []string // real user prompts, oldest first
	Model          string   // last model announced to the main thread
	LastAssistant  string   // text of the last assistant message
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
			if a := e.Attachment; a != nil && a.Type == "model" && a.Identity != nil && a.Identity.ModelID != "" {
				info.Model = a.Identity.ModelID
			}
		case "system":
			if e.Subtype == "compact_boundary" {
				info.CompactSummary = ""
				info.UserPrompts = nil
				info.LastAssistant = ""
			}
		case "assistant":
			if t := assistantText(e.Message); t != "" {
				info.LastAssistant = t
			}
		case "user":
			text := userText(e.Message)
			switch {
			case e.IsCompactSummary:
				info.CompactSummary = text
			case e.IsMeta || text == "" || IsSynthetic(text):
			default:
				info.UserPrompts = append(info.UserPrompts, text)
			}
		}
	}
	return info, sc.Err()
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
// the user: subagent hand-backs, slash-command wrappers, task notifications.
func IsSynthetic(text string) bool {
	t := strings.TrimSpace(text)
	for _, p := range []string{"<agent-message", "<command-", "<local-command", "<task-notification", "<system-reminder", "[SYSTEM NOTIFICATION"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}
