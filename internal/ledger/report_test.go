package ledger

import (
	"strings"
	"testing"
	"time"
)

func TestBuildReport(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	lines := []string{
		`{"ts":"` + now + `","kind":"decision","session_id":"a","scope":"main","trigger":"initial","confidence":0.8,"jev_choice":"high","chosen":"high","jev_cost_usd":0.001,"shadow":{"model":"new","chosen":"high","confidence":0.7}}`,
		`{"ts":"` + now + `","kind":"decision","session_id":"b","scope":"main","trigger":"fallback","chosen":"high","jev_cost_usd":0}`,
		`{"ts":"` + now + `","kind":"decision","session_id":"a","scope":"subagent","trigger":"agent","confidence":0.4,"jev_choice":"opus-low","chosen":"opus-medium","jev_cost_usd":0.001,"shadow":{"model":"new","chosen":"opus-high","confidence":0.5}}`,
		`{"ts":"` + now + `","kind":"usage","session_id":"a","scope":"main","routed":true,"tier":"high","model":"m","input_tokens":10,"cache_read_input_tokens":90,"output_tokens":5}`,
		`{"ts":"` + now + `","kind":"usage","session_id":"c","scope":"main","routed":false,"model":"m","input_tokens":50,"cache_read_input_tokens":50}`,
		`{"ts":"2020-01-01T00:00:00Z","kind":"decision","scope":"main","chosen":"max"}`,
		`{"ts":"` + now + `","kind":"decision","session_id":"a","scope":"subagent","trigger":"agent","confidence":0.9,"jev_choice":"opus-medium","chosen":"opus-medium","budget_cap":"opus-high"}`,
		`not json`,
	}
	rep, err := BuildReport(strings.NewReader(strings.Join(lines, "\n")), time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	m := rep.Scopes["main"]
	if m.Decisions != 2 || m.Fallbacks != 1 || m.FallbackRate != 0.5 || m.Tiers["max"] != 0 {
		t.Errorf("main = %+v", m)
	}
	if s := rep.Scopes["subagent"]; s.Escalated != 1 {
		t.Errorf("subagent = %+v", s)
	}
	if rep.RoutedCacheHit != 0.9 || rep.UnroutedCacheHit != 0.5 {
		t.Errorf("cache hit = %v / %v", rep.RoutedCacheHit, rep.UnroutedCacheHit)
	}
	if sh := rep.Shadow; sh == nil || sh.Compared != 2 || sh.Agree != 1 || sh.Disagreement["opus-medium→opus-high"] != 1 {
		t.Errorf("shadow = %+v", rep.Shadow)
	}
	var b strings.Builder
	rep.Markdown(&b)
	if !strings.Contains(b.String(), "fallback rate 50.0%") || !strings.Contains(b.String(), "1 decisions lowered to the cap") {
		t.Errorf("markdown:\n%s", b.String())
	}
}
