package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

const sse = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5000,"cache_creation_input_tokens":200,"output_tokens":1}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":42}}` + "\n\n" +
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

type upstream struct {
	mu     sync.Mutex
	bodies [][]byte
	auth   []string
	betas  []string
	// reply, when set, is sent instead of sse, in chunks of chunk bytes
	// (0: at once), as JSON unless it starts with "event:".
	reply string
	chunk int
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, b)
	u.auth = append(u.auth, r.Header.Get("Authorization"))
	u.betas = append(u.betas, strings.Join(r.Header.Values(HeaderBeta), ","))
	reply, chunk := u.reply, u.chunk
	u.mu.Unlock()
	w.Header().Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.42")
	w.Header().Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.13")
	w.Header().Set("Anthropic-Ratelimit-Unified-Grace-7d-Utilization", "0.5")
	if reply == "" {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
		return
	}
	if !strings.HasPrefix(reply, "event:") {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for len(reply) > 0 {
		n := len(reply)
		if chunk > 0 && chunk < n {
			n = chunk
		}
		io.WriteString(w, reply[:n])
		w.(http.Flusher).Flush()
		reply = reply[n:]
	}
}

func (u *upstream) last(t *testing.T) map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	var m map[string]any
	if err := json.Unmarshal(u.bodies[len(u.bodies)-1], &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func setup(t *testing.T) (*Proxy, *upstream, *httptest.Server) {
	t.Helper()
	up := &upstream{}
	us := httptest.NewServer(up)
	t.Cleanup(us.Close)
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Upstream = us.URL
	cfg.StateDir = dir
	cfg.Ledger = filepath.Join(dir, "ledger.jsonl")
	store := &catalog.Store{Path: "../../catalog.toml", StaleDays: 3650}
	p, err := New(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.bg.Wait)
	ps := httptest.NewServer(p)
	t.Cleanup(ps.Close)
	return p, up, ps
}

func post(t *testing.T, url string, headers map[string]string, body string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/v1/messages?beta=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer sk-ant-oat-test")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

const mainBody = `{"model":"jev","max_tokens":32000,"stream":true,"thinking":{"type":"adaptive","display":"omitted"},` +
	`"output_config":{"effort":"medium"},"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"sess-1\"}"},` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

func TestRewriteMainFromDecision(t *testing.T) {
	p, up, ps := setup(t)
	p.State.Update("sess-1", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"}
		return true
	})
	got := post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, mainBody)
	if got != sse {
		t.Errorf("SSE altered:\n%q", got)
	}
	m := up.last(t)
	if m["model"] != "claude-opus-5-5" || m["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Errorf("rewritten body = %v", m)
	}
	if up.auth[0] != "Bearer sk-ant-oat-test" {
		t.Errorf("auth not passed through: %q", up.auth[0])
	}
	// Session from metadata when the header is missing, default tier otherwise.
	post(t, ps.URL, nil, mainBody)
	if m := up.last(t); m["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Errorf("metadata session not used: %v", m["output_config"])
	}
	post(t, ps.URL, nil, strings.Replace(mainBody, "sess-1", "unknown", 1))
	if m := up.last(t); m["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("default tier not applied: %v", m["output_config"])
	}

	// Usage lands in the ledger and the session's context size is recorded.
	waitFor(t, func() bool {
		b, _ := os.ReadFile(p.Cfg.Ledger)
		return strings.Count(string(b), `"kind":"usage"`) == 3
	})
	b, _ := os.ReadFile(p.Cfg.Ledger)
	if !strings.Contains(string(b), `"output_tokens":42`) || !strings.Contains(string(b), `"cache_read_input_tokens":5000`) ||
		!strings.Contains(string(b), `"limits":{"5h":0.42,"7d":0.13}`) {
		t.Errorf("ledger = %s", b)
	}
	waitFor(t, func() bool {
		s, _ := p.State.Load("sess-1")
		return s.ContextTokens == 5252 && s.Model == "jev"
	})
	// The day's and the session's spend feed the budget cap.
	waitFor(t, func() bool {
		s, _ := p.State.Load("sess-1")
		return s.TotalUSD > 0 && s.TotalUSD == s.SpendUSD && p.State.SpentToday(time.Now()) > s.TotalUSD
	})
}

func TestPassthroughUntouched(t *testing.T) {
	_, up, ps := setup(t)
	body := strings.Replace(mainBody, `"jev"`, `"claude-opus-5-5"`, 1)
	post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, body)
	if got := string(up.bodies[0]); got != body {
		t.Errorf("non-routed body changed:\n%s", got)
	}
}

func TestHealth(t *testing.T) {
	p, up, ps := setup(t)
	p.Version = "v1.2.3"
	resp, err := http.Get(ps.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h struct{ Version string }
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil || h.Version != "v1.2.3" {
		t.Errorf("health: %+v %v", h, err)
	}
	if len(up.bodies) != 0 || !p.Idle(0) {
		t.Error("health request forwarded or counted as activity")
	}
}

func TestSubagentEffortBinding(t *testing.T) {
	p, up, ps := setup(t)
	p.State.Update("sess-2", func(s *state.Session) bool {
		s.PendingAgents = []state.PendingAgent{{Prompt: "Compute 3*7. Reply with only the number.",
			Decision:  state.Decision{Scope: "subagent", Tier: "opus-low", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "low"},
			CreatedAt: time.Now()}}
		return true
	})
	body := `{"model":"claude-opus-5-5","output_config":{"effort":"high"},"thinking":{"type":"adaptive"},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>x</system-reminder>"},` +
		`{"type":"text","text":"Compute 3*7.  Reply with only the number.\n"}]}]}`
	h := map[string]string{HeaderSession: "sess-2", HeaderAgent: "agent-1"}
	post(t, ps.URL, h, body)
	if m := up.last(t); m["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("subagent effort = %v", m["output_config"])
	}
	s, _ := p.State.Load("sess-2")
	if len(s.PendingAgents) != 0 || s.Agents["agent-1"].Tier != "opus-low" {
		t.Errorf("binding not recorded: %+v", s)
	}
	// Later turns of the same agent keep the binding.
	later := strings.Replace(body, `"messages":[`, `"messages":[{"role":"user","content":"other"},{"role":"assistant","content":"ok"},`, 1)
	post(t, ps.URL, h, later)
	if m := up.last(t); m["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("binding lost: %v", m["output_config"])
	}
	// An unrelated agent passes through.
	post(t, ps.URL, map[string]string{HeaderSession: "sess-2", HeaderAgent: "agent-2"}, body)
	if m := up.last(t); m["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("unbound agent rewritten: %v", m["output_config"])
	}

	// Sent a new message, the agent was decided again (the message hook):
	// the new decision applies at once, model included, to its alias
	// requests and to those asking for the session's model.
	p.State.Update("sess-2", func(s *state.Session) bool {
		s.Agents["agent-1"] = &state.Decision{Scope: "subagent", Tier: "sonnet-xhigh", Model: "claude-sonnet-5-5",
			APIID: "claude-sonnet-5-5", Effort: "xhigh", Trigger: TriggerResumeAgent}
		s.Agents["agent-3"] = &state.Decision{Scope: "subagent", Tier: "sonnet-high", Model: "claude-sonnet-5-5",
			APIID: "claude-sonnet-5-5", Effort: "high", Trigger: TriggerResumeAgent}
		return true
	})
	post(t, ps.URL, h, later)
	if m := up.last(t); m["model"] != "claude-sonnet-5-5" || m["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Errorf("resume decision not applied to an alias request: %v %v", m["model"], m["output_config"])
	}
	post(t, ps.URL, map[string]string{HeaderSession: "sess-2", HeaderAgent: "agent-3"}, strings.Replace(later, `"claude-opus-5-5"`, `"jev"`, 1))
	if m := up.last(t); m["model"] != "claude-sonnet-5-5" || m["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("resume decision not applied to a request for the session's model: %v %v", m["model"], m["output_config"])
	}
	// A tier the catalog retired still applies: the agent keeps what it was given.
	p.State.Update("sess-2", func(s *state.Session) bool {
		s.Agents["agent-3"] = &state.Decision{Scope: "subagent", Tier: "opus-retired", Model: "claude-opus-5-5", APIID: "claude-opus-5-5", Effort: "high"}
		return true
	})
	post(t, ps.URL, map[string]string{HeaderSession: "sess-2", HeaderAgent: "agent-3"}, strings.Replace(later, `"claude-opus-5-5"`, `"jev"`, 1))
	if m := up.last(t); m["model"] != "claude-opus-5-5" || m["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("binding on a retired tier lost: %v %v", m["model"], m["output_config"])
	}
}

func TestApplyEffortStripsForModelsWithoutEffort(t *testing.T) {
	c, _, _ := catalog.Load("../../catalog.toml", time.Now(), 3650)
	var fields map[string]json.RawMessage
	json.Unmarshal([]byte(`{"model":"x","output_config":{"effort":"high"},"thinking":{"type":"adaptive"},`+
		`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}}`), &fields)
	applyEffort(fields, c.Model("claude-haiku-4-5"), "", false)
	for _, k := range []string{"output_config", "thinking", "context_management"} {
		if _, ok := fields[k]; ok {
			t.Errorf("%s not stripped", k)
		}
	}
	// count_tokens without output_config: don't add one.
	json.Unmarshal([]byte(`{"model":"x"}`), &fields)
	delete(fields, "output_config")
	applyEffort(fields, c.Model("claude-opus-5-5"), "high", true)
	if _, ok := fields["output_config"]; ok {
		t.Error("output_config added to count_tokens")
	}
}

func TestCountTokensRewritten(t *testing.T) {
	_, up, ps := setup(t)
	req, _ := http.NewRequest("POST", ps.URL+"/v1/messages/count_tokens?beta=true", bytes.NewBufferString(`{"model":"jev","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if m := up.last(t); m["model"] != "claude-opus-5-5" {
		t.Errorf("count_tokens model = %v", m["model"])
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestLongContextBeta(t *testing.T) {
	p, up, ps := setup(t)
	const beta = "context-1m-2025-08-07"
	lastBeta := func() string { up.mu.Lock(); defer up.mu.Unlock(); return up.betas[len(up.betas)-1] }

	// Opus 5.5 has a native 1M window: no long-context beta (it defeats
	// the prompt cache), and one sent for "jev[1m]" is stripped.
	post(t, ps.URL, map[string]string{HeaderSession: "sess-1", HeaderBeta: "claude-code-20250219,effort-2025-11-24"}, mainBody)
	if got := lastBeta(); got != "claude-code-20250219,effort-2025-11-24,per-turn-control-2026-07-01" {
		t.Errorf("opus betas = %q", got)
	}
	post(t, ps.URL, map[string]string{HeaderSession: "sess-1", HeaderBeta: beta + ",effort-2025-11-24"},
		strings.Replace(mainBody, `"jev"`, `"jev[1m]"`, 1))
	if got := lastBeta(); got != "effort-2025-11-24,per-turn-control-2026-07-01" {
		t.Errorf("jev[1m] betas = %q", got)
	}
	// Routed onto a 200k model: the beta is stripped.
	p.State.Update("sess-h", func(s *state.Session) bool {
		s.Agents = map[string]*state.Decision{"agent-1": {Tier: "haiku", Model: "claude-haiku-4-5"}}
		return true
	})
	post(t, ps.URL, map[string]string{HeaderSession: "sess-h", HeaderAgent: "agent-1", HeaderBeta: "claude-code-20250219," + beta},
		strings.Replace(mainBody, "sess-1", "sess-h", 1))
	if m := up.last(t); m["model"] != "claude-haiku-4-5-20251001" {
		t.Fatalf("not routed to haiku: %v", m["model"])
	}
	if got := lastBeta(); got != "claude-code-20250219" {
		t.Errorf("haiku betas = %q", got)
	}
	// A session on a named model is never touched.
	post(t, ps.URL, map[string]string{HeaderSession: "sess-o", HeaderBeta: "claude-code-20250219"},
		strings.Replace(mainBody, `"jev"`, `"claude-opus-5-5"`, 1))
	if got := lastBeta(); got != "claude-code-20250219" {
		t.Errorf("non-routed betas = %q", got)
	}
	if s, _ := p.State.Load("sess-o"); s.Model != "" || s.Main != nil {
		t.Errorf("non-routed session state written: %+v", s)
	}
}

// turnBody builds a main request with the given conversation.
func turnBody(msgs ...string) string {
	return `{"model":"jev","max_tokens":32000,"stream":true,"output_config":{"effort":"xhigh"},` +
		`"metadata":{"user_id":"{\"session_id\":\"sess-t\"}"},"messages":[` + strings.Join(msgs, ",") + `]}`
}

func user(text string, cache bool) string {
	cc := ""
	if cache {
		cc = `,"cache_control":{"type":"ephemeral"}`
	}
	return `{"role":"user","content":[{"type":"text","text":` + strconvQuote(text) + cc + `}]}`
}

func asst(text string) string {
	return `{"role":"assistant","content":[{"type":"text","text":` + strconvQuote(text) + `}]}`
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestPerTurnEffort(t *testing.T) {
	p, up, ps := setup(t)
	h := map[string]string{HeaderSession: "sess-t"}
	setMain := func(effort string, pending bool) {
		p.State.Update("sess-t", func(s *state.Session) bool {
			s.Main = &state.Decision{Tier: effort, Model: "claude-opus-5-5", Effort: effort}
			if pending {
				s.PendingEffort = &state.PendingEffort{Effort: effort, Prompt: "p2"}
			}
			return true
		})
	}
	msgsOf := func(m map[string]any) []any { return m["messages"].([]any) }
	effortOf := func(m map[string]any) any { return m["output_config"].(map[string]any)["effort"] }

	// First request: the top-level effort becomes the epoch base, stated
	// after the first user message.
	setMain("xhigh", false)
	post(t, ps.URL, h, turnBody(user("p1", true)))
	m := up.last(t)
	ms := msgsOf(m)
	if effortOf(m) != "xhigh" || len(ms) != 2 || ms[1].(map[string]any)["output_config"].(map[string]any)["effort"] != "xhigh" {
		t.Fatalf("first request: %v", m)
	}
	if m["max_tokens"] != float64(128000) {
		t.Errorf("max_tokens not raised: %v", m["max_tokens"])
	}
	// A warm switch to low: the top-level effort stays xhigh and an
	// effort-only system message follows the new prompt.
	setMain("low", true)
	post(t, ps.URL, h, turnBody(user("p1", false), asst("a1"), user("p2", true)))
	m = up.last(t)
	ms = msgsOf(m)
	if effortOf(m) != "xhigh" || len(ms) != 5 {
		t.Fatalf("switch request: effort %v, %d messages", effortOf(m), len(ms))
	}
	sys := ms[4].(map[string]any)
	if sys["role"] != "system" || sys["output_config"].(map[string]any)["effort"] != "low" || len(sys["content"].([]any)) != 0 {
		t.Errorf("system message = %v", sys)
	}
	// Later requests re-insert both at the same places (cache_control moved).
	post(t, ps.URL, h, turnBody(user("p1", false), asst("a1"), user("p2", false), asst("a2"), user("p3", true)))
	ms = msgsOf(up.last(t))
	if len(ms) != 7 || ms[1].(map[string]any)["role"] != "system" || ms[4].(map[string]any)["role"] != "system" {
		t.Errorf("marks not re-inserted: %v", ms)
	}
	// Claude Code's own effort-only messages are dropped.
	post(t, ps.URL, h, turnBody(user("p1", false), `{"role":"system","content":[],"output_config":{"effort":"max"}}`, asst("a1"), user("p2", false)))
	ms = msgsOf(up.last(t))
	if len(ms) != 5 {
		t.Errorf("foreign effort message kept: %v", ms)
	}
	// A rewritten history (compaction) starts a new epoch at the current effort.
	post(t, ps.URL, h, turnBody(user("summary", false), user("p4", true)))
	if m := up.last(t); effortOf(m) != "low" || len(msgsOf(m)) != 3 {
		t.Errorf("after rewrite: effort %v, messages %v", effortOf(m), msgsOf(m))
	}
	if s, _ := p.State.Load("sess-t"); s.EffortBase != "low" || len(s.EffortMarks) != 1 {
		t.Errorf("epoch not reset: %+v", s)
	}
}

func TestPerTurnEffortRefused(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	us := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if strings.Contains(string(b), `"role":"system"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1: output_config.effort requires a model that supports per-turn effort"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse)
	}))
	defer us.Close()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Upstream, cfg.StateDir, cfg.Ledger = us.URL, dir, filepath.Join(dir, "ledger.jsonl")
	p, err := New(cfg, &catalog.Store{Path: "../../catalog.toml", StaleDays: 3650})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.bg.Wait)
	ps := httptest.NewServer(p)
	defer ps.Close()
	p.State.Update("sess-t", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "low", Model: "claude-opus-5-5", Effort: "low"}
		s.EffortBase = "xhigh"
		s.PendingEffort = &state.PendingEffort{Effort: "low"}
		return true
	})
	got := post(t, ps.URL, map[string]string{HeaderSession: "sess-t"}, turnBody(user("p1", false), asst("a1"), user("p2", true)))
	if got != sse {
		t.Fatalf("client saw %q", got)
	}
	if len(bodies) != 2 || strings.Contains(bodies[1], `"role":"system"`) || !strings.Contains(bodies[1], `"effort":"low"`) {
		t.Errorf("retry bodies: %v", bodies)
	}
	if s, _ := p.State.Load("sess-t"); !s.PerTurnRejected {
		t.Error("per-turn effort not turned off")
	}
}

// A session on a tier with a max_context (the main Haiku tier: Haiku 5.5's
// price step) goes to the next tier that fits once the context outgrows it,
// within the turn.
func TestSmallWindowTierOutgrown(t *testing.T) {
	p, up, ps := setup(t)
	p.State.Update("sess-1", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "haiku", Model: "claude-haiku-5-5", Effort: "low"}
		s.ContextTokens = 10_000
		return true
	})
	post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, mainBody)
	// Haiku 5.5 takes an effort and keeps adaptive thinking (it rejects
	// disabled thinking).
	if m := up.last(t); m["model"] != "claude-haiku-5-5" || m["thinking"] == nil || m["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("Haiku request = %v", m)
	}
	// The first response's usage lands first (its tap may still be
	// finishing when the client has read the whole stream).
	waitFor(t, func() bool { s, _ := p.State.Load("sess-1"); return s.ContextTokens == 5252 })
	p.bg.Wait()
	p.State.Update("sess-1", func(s *state.Session) bool { s.ContextTokens = 160_000; return true })
	post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, mainBody)
	if m := up.last(t); m["model"] != "claude-opus-5-5" || m["output_config"].(map[string]any)["effort"] != "low" {
		t.Errorf("outgrown Haiku request = %v", m)
	}
}

// A message's anchor follows what it says, not markers or fields that
// aren't rendered.
func TestAnchor(t *testing.T) {
	a := anchor(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}`))
	b := anchor(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello","citations":null}],"id":"x"}`))
	c := anchor(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello!"}]}`))
	if a == "" || a != b {
		t.Errorf("same message, different anchors: %s %s", a, b)
	}
	if a == c {
		t.Error("a changed text kept its anchor")
	}
}

// Message threads: a continue request carries only the new messages; the
// top-level effort stays, and an effort change is a statement after the new
// prompt. A create request replays the whole history with every mark in place.
func TestPerTurnEffortThreads(t *testing.T) {
	p, up, ps := setup(t)
	h := map[string]string{HeaderSession: "sess-t"}
	setMain := func(effort string, pending bool) {
		p.State.Update("sess-t", func(s *state.Session) bool {
			s.Main = &state.Decision{Tier: effort, Model: "claude-opus-5-5", Effort: effort}
			if pending {
				s.PendingEffort = &state.PendingEffort{Effort: effort, Prompt: "p"}
			}
			return true
		})
	}
	thread := func(kind string, msgs ...string) string {
		b := turnBody(msgs...)
		return strings.Replace(b, `"stream":true,`, `"stream":true,"thread":{"type":"`+kind+`","previous_message_id":"msg_1"},`, 1)
	}
	effortOf := func(m map[string]any) any { return m["output_config"].(map[string]any)["effort"] }
	msgsOf := func(m map[string]any) []any { return m["messages"].([]any) }

	setMain("xhigh", false)
	post(t, ps.URL, h, thread("create", user("p1", true)))
	if m := up.last(t); effortOf(m) != "xhigh" || len(msgsOf(m)) != 2 {
		t.Fatalf("create: %v", m)
	}
	// Continue with a switch to low: top-level stays xhigh, a statement follows p2.
	setMain("low", true)
	post(t, ps.URL, h, thread("continue", asst("a1"), user("p2", true)))
	m := up.last(t)
	ms := msgsOf(m)
	if effortOf(m) != "xhigh" || len(ms) != 3 || ms[2].(map[string]any)["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("continue with a switch: effort %v, %v", effortOf(m), ms)
	}
	// Next continue: nothing re-inserted (the thread holds it), no epoch reset.
	post(t, ps.URL, h, thread("continue", asst("a2"), user("p3", true)))
	if m := up.last(t); effortOf(m) != "xhigh" || len(msgsOf(m)) != 2 {
		t.Fatalf("plain continue: effort %v, %v", effortOf(m), msgsOf(m))
	}
	// A new thread replays everything: both statements at their places.
	post(t, ps.URL, h, thread("create", user("p1", false), asst("a1"), user("p2", false), asst("a2"), user("p3", true)))
	ms = msgsOf(up.last(t))
	if len(ms) != 7 || ms[1].(map[string]any)["role"] != "system" || ms[4].(map[string]any)["role"] != "system" {
		t.Errorf("replay: marks not in place: %v", ms)
	}
	if s, _ := p.State.Load("sess-t"); s.EffortBase != "xhigh" || len(s.EffortMarks) != 2 {
		t.Errorf("state: %+v", s)
	}
}

// A routed response names the custom model, so the transcript records the
// session's model and a resumed session stays routed; the model served
// stays in the ledger, and other responses are untouched.
func TestResponseNamesTheCustomModel(t *testing.T) {
	p, up, ps := setup(t)
	p.State.Update("sess-1", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"}
		return true
	})
	served := strings.Replace(sse, `"message":{`, `"message":{"model":"claude-opus-5-5","id":"msg_1",`, 1)
	up.reply, up.chunk = served, 7 // lines split across reads
	got := post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, mainBody)
	if want := strings.Replace(served, `"model":"claude-opus-5-5"`, `"model":"jev"`, 1); got != want {
		t.Errorf("stream:\n%q\nwant\n%q", got, want)
	}
	waitFor(t, func() bool {
		b, _ := os.ReadFile(p.Cfg.Ledger)
		return strings.Contains(string(b), `"model":"claude-opus-5-5"`) && strings.Contains(string(b), `"output_tokens":42`)
	})
	// Not routed: the model served is named.
	body := strings.Replace(mainBody, `"jev"`, `"claude-opus-5-5"`, 1)
	if got := post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, body); got != served {
		t.Errorf("passthrough stream altered:\n%q", got)
	}
	// A whole message.
	up.reply = `{"id":"msg_2","type":"message","model":"claude-opus-5-5","content":[{"type":"text","text":"model: x"}],"usage":{"input_tokens":3,"output_tokens":4}}`
	got = post(t, ps.URL, map[string]string{HeaderSession: "sess-1"}, strings.Replace(mainBody, `"stream":true,`, "", 1))
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil || m["model"] != "jev" || m["id"] != "msg_2" {
		t.Errorf("message = %s (%v)", got, err)
	}
}

func TestWorkflowAgentOwnLevel(t *testing.T) {
	p, up, ps := setup(t)
	var asked []string
	p.Decide = func(_ context.Context, c *catalog.Catalog, req router.Request) *state.Decision {
		asked = append(asked, req.State["task"].(string))
		tier := "sonnet-high"
		if strings.Contains(req.State["task"].(string), "tiny") {
			tier = "haiku"
		}
		t := c.Tier(catalog.ScopeSubagent, tier)
		return &state.Decision{Scope: "subagent", Tier: t.ID, Model: t.Model, Effort: t.Effort, Trigger: "workflow"}
	}
	p.State.Update("sess-w", func(s *state.Session) bool {
		s.Main = &state.Decision{Tier: "xhigh", Model: "claude-opus-5-5", Effort: "xhigh"}
		return true
	})
	body := `{"model":"jev","output_config":{"effort":"xhigh"},"thinking":{"type":"adaptive"},` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},` +
		`{"type":"text","text":"[Workflow harness — user request] The harness relays the user request:\n  ship it"},` +
		`{"type":"text","text":"[Workflow harness — computed task] The computed task text follows:\n  Review the diff of the parser\n  for off-by-one errors."}]}]}`
	wf := map[string]string{HeaderSession: "sess-w", HeaderAgent: "wf-1", HeaderAgentType: AgentTypeWorkflow}
	post(t, ps.URL, wf, body)
	if m := up.last(t); m["model"] != "claude-sonnet-5-5" || m["output_config"].(map[string]any)["effort"] != "high" {
		t.Errorf("workflow agent not routed on its own: %v %v", m["model"], m["output_config"])
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "parser\nfor off-by-one") || strings.Contains(asked[0], "ctx") || strings.Contains(asked[0], "ship it") {
		t.Errorf("task sent to Jev = %q", asked)
	}
	// Its later requests keep the decision, without asking again.
	post(t, ps.URL, wf, strings.Replace(body, `"messages":[`, `"messages":[{"role":"user","content":"x"},{"role":"assistant","content":"y"},`, 1))
	if m := up.last(t); m["model"] != "claude-sonnet-5-5" || len(asked) != 1 {
		t.Errorf("binding lost or decided again: %v, %d asks", m["model"], len(asked))
	}
	// A mechanical stage gets the cheap tier (Haiku 5.5 has the main thread's window).
	post(t, ps.URL, map[string]string{HeaderSession: "sess-w", HeaderAgent: "wf-2", HeaderAgentType: AgentTypeWorkflow},
		strings.Replace(body, "Review the diff of the parser", "a tiny lookup", 1))
	if m := up.last(t); m["model"] != "claude-haiku-5-5" {
		t.Errorf("mechanical workflow agent = %v", m["model"])
	}
	// Other agents asking for the session's model (forks) follow the main thread.
	post(t, ps.URL, map[string]string{HeaderSession: "sess-w", HeaderAgent: "fork-1", HeaderAgentType: "fork"}, body)
	if m := up.last(t); m["model"] != "claude-opus-5-5" || m["output_config"].(map[string]any)["effort"] != "xhigh" || len(asked) != 2 {
		t.Errorf("fork rerouted: %v %v", m["model"], m["output_config"])
	}
	// Off, or in a pinned session, the workflow agent keeps the session's model.
	p.Cfg.Features.WorkflowAgentsOwnLevel = false
	post(t, ps.URL, map[string]string{HeaderSession: "sess-w", HeaderAgent: "wf-3", HeaderAgentType: AgentTypeWorkflow}, body)
	if m := up.last(t); m["model"] != "claude-opus-5-5" || len(asked) != 2 {
		t.Errorf("switch off ignored: %v", m["model"])
	}
	p.Cfg.Features.WorkflowAgentsOwnLevel = true
	p.State.Update("sess-w", func(s *state.Session) bool { s.PinModel = "claude-opus-5-5"; return true })
	post(t, ps.URL, map[string]string{HeaderSession: "sess-w", HeaderAgent: "wf-4", HeaderAgentType: AgentTypeWorkflow}, body)
	if len(asked) != 2 {
		t.Errorf("pinned session rerouted a workflow agent")
	}
}
