package transcript

import (
	"os"
	"path/filepath"
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
