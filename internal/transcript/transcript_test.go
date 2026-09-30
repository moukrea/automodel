package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte(`{"type":"user","message":{"role":"user","content":"old prompt"}}
{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"jev"}}}
{"type":"system","subtype":"compact_boundary"}
{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"the summary"}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"},{"type":"text","text":"new prompt"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}
{"type":"user","message":{"role":"user","content":"<agent-message from=\"a\">r</agent-message>"}}
{"type":"user","isSidechain":true,"message":{"role":"user","content":"subagent prompt"}}
`), 0o644)
	info, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "jev" || info.CompactSummary != "the summary" || len(info.UserPrompts) != 1 || info.UserPrompts[0] != "new prompt" {
		t.Errorf("info = %+v", info)
	}
}

func TestMidTurnAndQueuedPrompts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	write := func(lines ...string) Info {
		os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		info, err := Read(p)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	prompt := `{"type":"user","message":{"role":"user","content":"Add the price column to the export"}}`
	toolUse := `{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"text","text":"Editing the exporter:"},{"type":"tool_use","id":"t1","name":"Edit","input":{}}]}}`
	toolResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`
	queued := `{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"and convert the prices to euros"}}`
	done := `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Done."}]}}`
	turnEnd := `{"type":"system","subtype":"turn_duration"}`

	// Claude is between two tool calls: a prompt now was typed mid-turn;
	// the queued prompt counts as a user prompt (it never becomes a user line).
	if info := write(prompt, toolUse, toolResult, queued); !info.MidTurn || len(info.UserPrompts) != 2 || info.UserPrompts[1] != "and convert the prices to euros" {
		t.Errorf("mid-turn: %+v", info)
	}
	for name, lines := range map[string][]string{
		"turn ended":         {prompt, toolUse, toolResult, done, turnEnd},
		"end_turn, no stamp": {prompt, toolUse, toolResult, done},
		"interrupted":        {prompt, toolUse, `{"type":"user","message":{"role":"user","content":"[Request interrupted by user for tool use]"}}`},
		"compacted":          {prompt, toolUse, `{"type":"system","subtype":"compact_boundary"}`},
		"no tool":            {prompt, done},
	} {
		if info := write(lines...); info.MidTurn {
			t.Errorf("%s: read as mid-turn", name)
		}
	}
	// Notifications and messages from other sessions are not user prompts.
	info := write(prompt,
		`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"<task-notification>done</task-notification>"}}`,
		`{"type":"attachment","attachment":{"type":"queued_command","commandMode":"prompt","prompt":"<cross-session-message from=\"x\">hi</cross-session-message>"}}`,
		`{"type":"user","message":{"role":"user","content":"Another Claude session sent a message: the build is green"}}`)
	if len(info.UserPrompts) != 1 {
		t.Errorf("prompts = %q", info.UserPrompts)
	}
	if !IsPeer("  <cross-session-message from=\"a\">x</cross-session-message>") || !IsPeer("Another Claude session sent a message: hi") || IsPeer("tell another Claude session to rebase") {
		t.Error("IsPeer")
	}
}
