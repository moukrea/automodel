package router

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataOnlySendsNoText(t *testing.T) {
	secret := "SECRETWORD"
	st := map[string]any{
		"phase":              "warm",
		"task":               "Why do we oversell stock? Fix the race in checkout " + secret,
		"recent_prompts":     []string{"explain 409 " + secret, "write the commit message"},
		"last_assistant":     "The cause is ... " + secret,
		"compaction_summary": "We fixed " + secret,
		"current":            map[string]any{"tier": "low", "effort": "low"},
		"repo":               map[string]any{"languages": []string{"Python"}, "files": 12, "diff_stat": "orders.py " + secret, "claude_md_head": secret},
		"work_in_progress":   map[string]any{"goal": "Fix the oversell race " + secret, "level": "xhigh"},
	}
	out := MetadataOnly(st)
	b, _ := json.Marshal(out)
	s := string(b)
	if strings.Contains(s, secret) || strings.Contains(s, "oversell") || strings.Contains(s, "orders.py") {
		t.Fatalf("text leaked: %s", s)
	}
	f := out["task_features"].(map[string]any)
	hints := strings.Join(f["kind_hints"].([]string), ",")
	if !strings.Contains(hints, "bug") || !strings.Contains(hints, "concurrency") || f["asks_question"] != true {
		t.Errorf("task features = %v", f)
	}
	if out["phase"] != "warm" || out["current"] == nil || out["repo"].(map[string]any)["files"] != 12 || out["work_in_progress"].(map[string]any)["level"] != "xhigh" {
		t.Errorf("metadata dropped: %s", s)
	}
}
