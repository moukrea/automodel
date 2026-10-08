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
