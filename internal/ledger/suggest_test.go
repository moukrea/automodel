package ledger

import (
	"strings"
	"testing"
)

func TestSuggest(t *testing.T) {
	rank := map[string]int{"low": 1, "medium": 2, "high": 3, "xhigh": 4, "max": 5}
	o := SuggestOptions{Rank: func(_, t string) int { return rank[t] }, Floor: "high"}
	var ds []Decision
	add := func(repo, sid, trigger, chosen, cause string, sig map[string]any) {
		ds = append(ds, Decision{SessionID: sid, Scope: "main", Repo: repo, Trigger: trigger, Chosen: chosen, Cause: cause, Signals: sig})
	}
	// /a: Jev picks low, the user pins xhigh, four times; one "think harder".
	for _, sid := range []string{"s1", "s2", "s3", "s4"} {
		add("/a", sid, "initial", "low", "", nil)
		add("/a", sid, "pinned", "xhigh", "/effort", nil)
		add("/a", sid, "pinned", "xhigh", "/effort", nil) // same pin again: counted once
	}
	add("/a", "s5", "initial", "medium", "", nil)
	add("/a", "s5", "warm", "high", "", map[string]any{"asks_more_thinking": true})
	// /b: pins below Jev's pick don't count.
	for _, sid := range []string{"t1", "t2", "t3", "t4", "t5"} {
		add("/b", sid, "initial", "max", "", nil)
		add("/b", sid, "pinned", "low", "prompt", nil)
	}
	o.Flagged = [][2]string{{"main", "xhigh"}, {"main", "xhigh"}, {"main", "xhigh"}, {"main", "xhigh"}, {"main", "xhigh"}, {"main", "low"}}

	ss := Suggest(ds, o)
	if len(ss) != 2 {
		t.Fatalf("suggestions = %+v", ss)
	}
	if s := ss[0]; s.Repo != "/a" || s.Events != 5 || !strings.Contains(s.Text, `min_tier = "high"`) ||
		!strings.Contains(s.Why, "4 pins above Jev's pick (xhigh=4; by /effort=4)") || !strings.Contains(s.Why, `1 "think harder"`) {
		t.Errorf("repo suggestion = %+v", s)
	}
	if s := ss[1]; !strings.Contains(s.Text, "main/xhigh") || s.Events != 5 {
		t.Errorf("flagged suggestion = %+v", s)
	}
	o.RepoFloor = func(string) string { return "high" }
	if ss := Suggest(ds, o); len(ss) != 1 {
		t.Errorf("repo already at the floor still suggested: %+v", ss)
	}
	var b strings.Builder
	(&Report{Suggestions: ss}).Markdown(&b)
	if !strings.Contains(b.String(), "## Suggestions") {
		t.Errorf("markdown:\n%s", b.String())
	}
}
