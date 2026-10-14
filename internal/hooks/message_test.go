package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/state"
)

// writeAgent writes a subagent's meta and transcript: turns messages, each
// answered by one request of context tokens read from the cache, the last
// one at last.
func writeAgent(t *testing.T, dir, id, typ, name string, turns, context int, model string, last time.Time) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	meta, _ := json.Marshal(map[string]any{"agentType": typ, "description": "Root-cause a flaky test", "name": name})
	os.WriteFile(filepath.Join(dir, "agent-"+id+".meta.json"), meta, 0o600)
	var b strings.Builder
	for i := 0; i < turns; i++ {
		ts := last.Add(time.Duration(i-turns+1) * time.Minute)
		u, _ := json.Marshal(map[string]any{"type": "user", "timestamp": ts, "message": map[string]any{"content": fmt.Sprintf("step %d", i)}})
		a, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{"model": model,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 300000, "cache_read_input_tokens": context}}})
		b.WriteString(string(u) + "\n" + string(a) + "\n")
	}
	os.WriteFile(filepath.Join(dir, "agent-"+id+".jsonl"), []byte(b.String()), 0o600)
}

func TestMessageHook(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("sonnet-xhigh", 0.9)}}
	env := setup(t, fj)
	sid := "s7"
	markJev(t, env, sid)
	env.State.Update(sid, func(s *state.Session) bool {
		s.Main = &state.Decision{Scope: "main", Tier: "xhigh", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "xhigh", Mode: "ultracode"}
		return true
	})
	root := t.TempDir()
	tp := filepath.Join(root, sid+".jsonl")
	dir := filepath.Join(root, sid, "subagents")
	now := time.Now()
	// Resumed after a restart, it asks for the session's model: it ran the
	// main decision, Opus xhigh. Each turn costs about $6, far more than
	// rewriting its 300k-token context on Sonnet.
	writeAgent(t, dir, "a9e28da00fe9cf0fe", "general-purpose", "flake-hunter", 10, 300_000, "jev", now.Add(-10*time.Minute))
	send := func(to, msg string) {
		run(t, env, "message", map[string]any{"session_id": sid, "tool_name": "SendMessage", "cwd": root, "transcript_path": tp,
			"tool_input": map[string]any{"to": to, "message": msg}})
	}
	send("flake-hunter", "Rebase your three PRs on main and check CI.")
	sess, _ := env.State.Load(sid)
	d := sess.Agents["a9e28da00fe9cf0fe"]
	if d == nil || d.Tier != "sonnet-xhigh" || d.APIID != "claude-sonnet-5-5" || d.Trigger != TriggerResumeAgent {
		l, _ := os.ReadFile(env.Cfg.Ledger)
		t.Fatalf("resumed agent decision = %+v\n%s", d, l)
	}
	if st, _ := fj.last().State.(map[string]any); st["task"] != "Rebase your three PRs on main and check CI." || st["description"] != "Root-cause a flaky test" {
		t.Errorf("state sent to Jev = %v", st)
	}

	// A warm agent whose turn costs less than its context's rewrite stays
	// on its model: 900k tokens on Opus, a cheap turn each time.
	writeAgent(t, dir, "ab0000000000000001", "general-purpose", "reader", 10, 900_000, "claude-opus-5-5", now.Add(-time.Minute))
	env.State.Update(sid, func(s *state.Session) bool {
		s.Agents["ab0000000000000001"] = &state.Decision{Scope: "subagent", Tier: "opus-xhigh", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "xhigh"}
		return true
	})
	os.WriteFile(filepath.Join(dir, "agent-ab0000000000000001.jsonl"), func() []byte {
		var b strings.Builder
		for i := 0; i < 10; i++ {
			u, _ := json.Marshal(map[string]any{"type": "user", "timestamp": now, "message": map[string]any{"content": "go"}})
			a, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": now.Add(-time.Minute), "message": map[string]any{"model": "claude-opus-5-5",
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 10, "cache_read_input_tokens": 900_000}}})
			b.WriteString(string(u) + "\n" + string(a) + "\n")
		}
		return []byte(b.String())
	}(), 0o600)
	send("reader", "One more file to read.")
	sess, _ = env.State.Load(sid)
	if d := sess.Agents["ab0000000000000001"]; d == nil || d.Model != "claude-opus-5-5" {
		t.Errorf("a switch that can't pay back its cache rewrite was taken: %+v", d)
	}

	// Forks keep the parent's model; an unknown recipient and the switch
	// turned off do nothing.
	writeAgent(t, dir, "ac0000000000000002", "fork", "", 3, 200_000, "jev", now)
	send("ac0000000000000002", "Try the other approach.")
	send("nobody", "hello")
	env.Cfg.Features.ResumedAgentsOwnLevel = false
	writeAgent(t, dir, "ad0000000000000003", "general-purpose", "off", 3, 200_000, "jev", now)
	send("off", "Carry on.")
	sess, _ = env.State.Load(sid)
	for _, id := range []string{"ac0000000000000002", "ad0000000000000003"} {
		if d := sess.Agents[id]; d != nil {
			t.Errorf("agent %s got a decision: %+v", id, d)
		}
	}
}

// A teammate (agent teams) is keyed name@team in its requests, and a move
// to Haiku past 100k tokens is not free: the whole context is written again
// at 5x Haiku's list price (live: a 507k-context teammate on Opus max read
// as a free move to Haiku on a 0.16-confidence reading, and the decision was
// stored under its transcript's ID, where the proxy never looked).
func TestMessageHookTeammate(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("haiku", 0.16)}}
	env := setup(t, fj)
	sid := "s8"
	markJev(t, env, sid)
	key := "m01-physics@session-s8"
	env.State.Update(sid, func(s *state.Session) bool {
		s.Main = &state.Decision{Scope: "main", Tier: "xhigh", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "xhigh"}
		s.Agents = map[string]*state.Decision{key: {Scope: "subagent", Tier: "opus-max", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "max"}}
		return true
	})
	root := t.TempDir()
	tp := filepath.Join(root, sid+".jsonl")
	dir := filepath.Join(root, sid, "subagents")
	now := time.Now()
	id := "am01-physics-1c49b8837aaacc61"
	writeAgent(t, dir, id, "m01-physics", "m01-physics", 10, 507_000, "claude-opus-5-5", now.Add(-5*time.Minute))
	meta, _ := json.Marshal(map[string]any{"agentType": "m01-physics", "description": "Native kart physics solver", "name": "m01-physics",
		"taskKind": "in_process_teammate", "teamName": "session-s8"})
	os.WriteFile(filepath.Join(dir, "agent-"+id+".meta.json"), meta, 0o600)
	run(t, env, "message", map[string]any{"session_id": sid, "tool_name": "SendMessage", "cwd": root, "transcript_path": tp,
		"tool_input": map[string]any{"to": "m01-physics", "message": "Check the drift constants against the new trace."}})
	sess, _ := env.State.Load(sid)
	if d := sess.Agents[id]; d != nil {
		t.Errorf("decision stored under the transcript ID, which the proxy never sees: %+v", d)
	}
	d := sess.Agents[key]
	if d == nil || d.Trigger != TriggerResumeAgent {
		t.Fatalf("teammate decision under %s = %+v", key, d)
	}
	// The reading (haiku 0.3, sonnet-low 0.7) says sonnet-low is enough;
	// Haiku, cheaper to rewrite, is not (0.3).
	if d.Model == "claude-haiku-5-5" || d.Tier != "sonnet-low" {
		l, _ := os.ReadFile(env.Cfg.Ledger)
		t.Errorf("507k-context teammate moved to %s (%s), want sonnet-low: %+v\n%s", d.Tier, d.Model, d, l)
	}
}

func TestAgentSwitchCostLongContext(t *testing.T) {
	env := setup(t, &fakeJev{})
	cur := &state.Decision{Model: "claude-opus-5-5", Effort: "max"}
	sc := agentSwitchCost(env, cur, 507_000)
	haiku := env.Catalog.Tier("subagent", "haiku")
	// 1h writes: 507k × (1.00 − 0.20) / 1e6 past 100k, not 507k × (0.20 − 0.20).
	if got := sc(haiku); got < 0.39 || got > 0.42 {
		t.Errorf("switch to Haiku at 507k = $%.3f, want about $0.41", got)
	}
	if got := agentSwitchCost(env, cur, 50_000)(haiku); got != 0 {
		t.Errorf("switch to Haiku at 50k = $%.3f, want 0 (short-prompt prices)", got)
	}
}

// A resumed agent on Opus max that Jev reads split between two Sonnet levels
// moves down: the lower tier is surely enough even if no single level is
// (live: teammates stayed on opus-max, read 0 by Jev, at confidence 0.53-0.79
// with sonnet-xhigh or below at 0.84-0.85).
func TestMessageHookCumulativeDown(t *testing.T) {
	for _, on := range []bool{true, false} {
		fj := &fakeJev{answers: []fa{answer("sonnet-high", 0.5)}}
		env := setup(t, fj)
		env.Cfg.Features.CumulativeDownConfidence = on
		sid := "s9"
		markJev(t, env, sid)
		env.State.Update(sid, func(s *state.Session) bool {
			s.Main = &state.Decision{Scope: "main", Tier: "xhigh", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "xhigh"}
			s.Agents = map[string]*state.Decision{"ae0000000000000009": {Scope: "subagent", Tier: "opus-max", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "max"}}
			return true
		})
		root := t.TempDir()
		dir := filepath.Join(root, sid, "subagents")
		writeAgent(t, dir, "ae0000000000000009", "general-purpose", "render", 10, 300_000, "claude-opus-5-5", time.Now().Add(-5*time.Minute))
		run(t, env, "message", map[string]any{"session_id": sid, "tool_name": "SendMessage", "cwd": root, "transcript_path": filepath.Join(root, sid+".jsonl"),
			"tool_input": map[string]any{"to": "render", "message": "Port the bloom pass from the dumped shader."}})
		sess, _ := env.State.Load(sid)
		d := sess.Agents["ae0000000000000009"]
		if moved := d != nil && d.Tier != "opus-max"; moved != on {
			l, _ := os.ReadFile(env.Cfg.Ledger)
			t.Errorf("cumulative %v: decision %+v\n%s", on, d, l)
		}
	}
}
