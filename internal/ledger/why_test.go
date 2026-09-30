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

	// The work in progress: held at its level, or a separate prompt's own.
	ds = []Decision{
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"medium": 0.85, "xhigh": 0.15},
			Chosen: "xhigh", JevChoice: "medium", Model: "claude-opus-5-5", Effort: "xhigh", Confidence: 0.7, Kept: true, KeepReason: "same tier",
			Relation: map[string]float64{"extend": 0.94, "continue": 0.04, "new_task": 0.02}, WorkTier: "xhigh", Hold: "follow-up of the work in progress (extend 0.94)"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.95, "medium": 0.05},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.9,
			Relation: map[string]float64{"wrap_up": 0.97, "continue": 0.03}, WorkTier: "xhigh"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "low", Kept: false, Skipped: true,
			Hold: "go-ahead: back to the work in progress", WorkTier: "xhigh", Chosen: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.9, "medium": 0.1},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.8, Explicit: map[string]float64{"effort_low": 0.93},
			Relation: map[string]float64{"extend": 0.9}, WorkTier: "xhigh", Hold: "explicit effort low", Work: "set"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.95, "medium": 0.05},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.9,
			Relation: map[string]float64{"new_task": 0.96}, WorkTier: "xhigh", Work: "new", Pauses: true},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "low", Probs: map[string]float64{"low": 0.9, "medium": 0.1},
			Chosen: "pinned", JevChoice: "low", Model: "claude-sonnet-5-5", Effort: "xhigh", Confidence: 0.8,
			Relation: map[string]float64{"resume": 0.93}, WorkTier: "low", PausedTier: "xhigh", Hold: "back to the paused work (resume 0.93)", Work: "resumed"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.95, "medium": 0.05},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.9,
			Relation: map[string]float64{"aside": 0.9, "side_question": 0.1}, WorkTier: "xhigh"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.95, "medium": 0.05},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.9,
			Relation: map[string]float64{"wrap_up": 0.95}, WorkTier: "xhigh", Work: "done"},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "xhigh", Probs: map[string]float64{"low": 0.95, "medium": 0.05},
			Chosen: "low", JevChoice: "low", Model: "claude-opus-5-5", Effort: "low", Confidence: 0.9,
			Relation: map[string]float64{"side_question": 0.95}, WorkTier: "xhigh", WorkDone: true},
		{TS: time.Now(), SessionID: "cccc3333", Scope: "main", Trigger: "warm", Warm: true, From: "low", Kept: false, Skipped: true,
			Hold: "go-ahead: back to the work in progress", WorkTier: "low", PausedTier: "xhigh", Work: "resumed", Chosen: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"},
	}
	b.Reset()
	WriteWhy(&b, ds, WhyOptions{Rank: func(_, t string) int { return rank[t] }})
	out = b.String()
	for _, want := range []string{
		"relation: extend 0.94, continue 0.04 · follow-up of the work in progress (extend 0.94) · work in progress xhigh",
		"relation: wrap_up 0.97, continue 0.03 · separate from the work in progress (xhigh): its own level, lower",
		"→ opus-5.5·xhigh (was low) without asking Jev: go-ahead: back to the work in progress",
		"asks in words: effort_low 0.93 · explicit effort low · work in progress xhigh · sets the work in progress",
		"relation: new_task 0.96 · separate from the work in progress (xhigh): its own level, lower · starts a new work in progress, pausing the one at xhigh",
		"sonnet-5.5·xhigh",
		"relation: resume 0.93 · back to the paused work (resume 0.93) · work in progress low · resumes the paused work (xhigh)",
		"relation: aside 0.90, side_question 0.10 · an aside: its own level for this turn, the work in progress (xhigh) unchanged, lower",
		"relation: wrap_up 0.95 · separate from the work in progress (xhigh): its own level, lower · marks the work in progress done",
		"relation: side_question 0.95 · the work in progress (xhigh) was wrapped up: its own level, lower",
		"without asking Jev: go-ahead: back to the work in progress (resumes the paused work)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if ShortModel("claude-haiku-4-5-20251001") != "haiku-4.5" || ShortModel("claude-opus-5-5") != "opus-5.5" {
		t.Error("ShortModel")
	}
}
