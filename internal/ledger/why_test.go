package ledger

import (
	"strings"
	"testing"
	"time"
)

func TestWhy(t *testing.T) {
	cont := 0.19
	ds := []Decision{
		{TS: time.Now(), SessionID: "aaaa1111", Scope: "main", Trigger: "initial", Probs: map[string]float64{"low": 1}, Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 1, LatencyMS: 700},
		{TS: time.Now(), SessionID: "bbbb2222", Scope: "main", Trigger: "warm", Warm: true, From: "low", Probs: map[string]float64{"low": 0.05, "xhigh": 0.94, "high": 0.01},
			Chosen: "xhigh", JevChoice: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh", Confidence: 0.87, ContinuesP: &cont, GainUSD: 0.12},
		{TS: time.Now(), SessionID: "bbbb2222", Scope: "main", Trigger: "warm", Warm: true, Kept: true, Skipped: true, KeepReason: "go-ahead: continues the work in progress", Chosen: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"},
		{TS: time.Now(), SessionID: "bbbb2222", Scope: "main", Trigger: "pinned", Cause: "/effort", Chosen: "high", Model: "claude-opus-5-5", Effort: "high"},
	}
	sid, got := SessionDecisions(ds, "", 5)
	if sid != "bbbb2222" || len(got) != 3 {
		t.Fatalf("session %s, %d decisions", sid, len(got))
	}
	rank := map[string]int{"low": 0, "medium": 1, "high": 2, "xhigh": 3, "max": 4}
	var b strings.Builder
	WriteWhy(&b, got, WhyOptions{Rank: func(_, t string) int { return rank[t] }})
	out := b.String()
	for _, want := range []string{"← pick", "opus-5.5·xhigh (was low)", "continues the work: 0.19", "expected gain $0.12",
		"keeps opus-5.5·xhigh without asking Jev: go-ahead", "pinned by /effort"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "  low ") > strings.Index(out, "  high ") {
		t.Errorf("tiers not in rank order:\n%s", out)
	}
	if ShortModel("claude-haiku-4-5-20251001") != "haiku-4.5" || ShortModel("claude-opus-5-5") != "opus-5.5" {
		t.Error("ShortModel")
	}
}
