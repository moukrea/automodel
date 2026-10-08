// Package proxy is the local reverse proxy in front of api.anthropic.com. It
// relays everything untouched (auth in passthrough, SSE unbuffered) except
// requests for the routed model ID, whose model and effort it rewrites from
// the decision the hooks stored for the session.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/state"
)

const (
	HeaderSession = "X-Claude-Code-Session-Id"
	HeaderAgent   = "X-Claude-Code-Agent-Id"
	HeaderBeta    = "Anthropic-Beta"

	// TriggerResumeAgent marks a subagent decision taken when the agent was
	// sent a new message (hooks.TriggerResumeAgent).
	TriggerResumeAgent = "resume_agent"

	maxBody       = 64 << 20
	stateThrottle = 20 * time.Second
	maxBindings   = 50000

	// HealthPath answers the proxy's own version (automodel doctor); it is
	// never forwarded.
	HealthPath = "/automodel/health"
)

type Proxy struct {
	Cfg     *config.Config
	Catalog *catalog.Store
	State   state.Store
	Ledger  ledger.Ledger
	Debug   bool
	Version string // reported on HealthPath
	dumpN   atomic.Int64

	upstream *url.URL
	rp       *httputil.ReverseProxy

	inflight atomic.Int64
	lastReq  atomic.Int64 // unix nanoseconds

	mu      sync.Mutex
	bg      sync.WaitGroup       // background state writes (Wait in tests)
	touched map[string]time.Time // session -> last state write
	// clientEffort is the last effort Claude Code sent per session (pins).
	clientEffort map[string]string
	tried        map[string]bool // agent IDs whose first message was matched against pending Agent calls
}

func New(cfg *config.Config, cat *catalog.Store) (*Proxy, error) {
	up, err := url.Parse(cfg.Upstream)
	if err != nil {
		return nil, err
	}
	p := &Proxy{
		Cfg: cfg, Catalog: cat, State: state.Store{Dir: cfg.StateDir}, Ledger: ledger.Ledger{Path: cfg.Ledger},
		upstream: up, touched: map[string]time.Time{}, tried: map[string]bool{},
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(up)
			r.Out.Host = up.Host
			// Let the transport negotiate compression and decompress
			// transparently, so the usage tap can read message responses.
			if strings.HasSuffix(r.In.URL.Path, "/v1/messages") {
				r.Out.Header.Del("Accept-Encoding")
			}
		},
		Transport:      &retryTransport{base: http.DefaultTransport, p: p},
		FlushInterval:  -1,
		ModifyResponse: p.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("upstream %s %s: %v", r.Method, r.URL.Path, err)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"automodel proxy: upstream unreachable"}}`)
		},
	}
	return p, nil
}

type ctxKey struct{}

// route is what the proxy decided for one request; the response tap uses it.
type route struct {
	sessionID string
	agentID   string
	scope     string
	routed    bool
	tier      string
	model     string
	effort    string
	path      string
	// asked is the custom model ID the request named ("": another model):
	// the response says it served that model (see servedAs).
	asked string

	plainBody   []byte // the request without per-turn effort, for retryTransport
	perTurnBeta string
}

func cloneFields(f map[string]json.RawMessage) map[string]json.RawMessage {
	c := make(map[string]json.RawMessage, len(f))
	for k, v := range f {
		c[k] = v
	}
	return c
}

// Idle reports whether no request is in flight and none started for d.
func (p *Proxy) Idle(d time.Duration) bool {
	return p.inflight.Load() == 0 && time.Since(time.Unix(0, p.lastReq.Load())) > d
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == HealthPath {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"version": p.Version, "pid": os.Getpid()})
		return
	}
	p.inflight.Add(1)
	p.lastReq.Store(time.Now().UnixNano())
	defer p.inflight.Add(-1)
	if r.Method != http.MethodPost || !strings.Contains(r.Header.Get("Content-Type"), "json") || r.Body == nil {
		p.rp.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
	r.Body.Close()
	if err != nil || len(body) > maxBody {
		http.Error(w, "automodel proxy: request body too large or unreadable", http.StatusRequestEntityTooLarge)
		return
	}
	rt := &route{path: r.URL.Path, agentID: r.Header.Get(HeaderAgent)}
	if nb, ok := p.rewrite(r, body, rt); ok {
		body = nb
	}
	if dir := os.Getenv("AUTOMODEL_DUMP_DIR"); dir != "" && !strings.HasSuffix(r.URL.Path, "/count_tokens") {
		// Debugging cache misses: every request as sent upstream, to diff.
		hdr := map[string]string{}
		for k, v := range r.Header {
			if lk := strings.ToLower(k); lk == "authorization" || lk == "x-api-key" || strings.Contains(lk, "cookie") {
				hdr[k] = "[redacted]"
			} else {
				hdr[k] = strings.Join(v, ", ")
			}
		}
		h, _ := json.Marshal(map[string]any{"path": r.URL.Path, "routed": rt.routed, "headers": hdr})
		n := p.dumpN.Add(1)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d-%s.json", n, short(rt.sessionID))), body, 0o600)
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d-%s.head", n, short(rt.sessionID))), h, 0o600)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Del("Content-Length")
	p.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, rt)))
}

// rewrite returns the new body when the request must change.
func (p *Proxy) rewrite(r *http.Request, body []byte, rt *route) ([]byte, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, false
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil || model == "" {
		return nil, false
	}
	rt.model = model
	rt.sessionID = p.sessionID(r, fields)
	rt.scope = catalog.ScopeMain
	if rt.agentID != "" {
		rt.scope = catalog.ScopeSubagent
	}
	cat, err := p.Catalog.Get()
	if err != nil {
		log.Printf("catalog unavailable, passing through: %v", err)
		return nil, false
	}
	countTokens := strings.HasSuffix(r.URL.Path, "/count_tokens")

	var dec *state.Decision
	custom := strings.TrimSuffix(model, "[1m]") == p.Cfg.CustomModelID
	if custom && p.Cfg.Features.NameCustomModel {
		rt.asked = p.Cfg.CustomModelID
	}
	switch {
	case custom:
		if rt.scope == catalog.ScopeMain && !countTokens {
			ce := clientEffort(fields)
			if p.Debug {
				log.Printf("client effort %s: %q (top-level %s)", short(rt.sessionID), ce, string(fields["output_config"]))
			}
			p.observeClientEffort(cat, rt.sessionID, ce)
		}
		dec = p.decisionFor(cat, rt, fields, len(body))
		rt.routed = true
		if rt.scope == catalog.ScopeMain && !countTokens {
			p.touch(rt.sessionID, func(s *state.Session) {
				if s.Model != model {
					s.Model, s.ModelSource = model, "proxy"
				}
				s.LastAPIAt = time.Now()
			})
		}
	case rt.agentID != "" && rt.sessionID != "":
		// A subagent whose model the agent hook set by alias: only the
		// effort, which the Agent tool can't carry, is ours to apply. A
		// decision taken when the agent was sent a new message applies
		// whole: the alias it was spawned with is no longer its level.
		b := p.binding(rt.sessionID, rt.agentID, fields)
		if b == nil || (b.APIID != model && b.Trigger != TriggerResumeAgent) || cat.Model(b.Model) == nil {
			return nil, false
		}
		dec = b
		rt.routed = b.APIID != model
	default:
		return nil, false
	}
	m := cat.Model(dec.Model)
	if m == nil {
		return nil, false
	}
	rt.tier, rt.model, rt.effort = dec.Tier, m.APIID, dec.Effort
	fields["model"], _ = json.Marshal(m.APIID)
	// Only the long-context beta a model needs is sent: on a model with a
	// native 1M window the beta is useless, and it defeats the prompt cache.
	setBeta(r.Header, cat.Meta.LongContextBeta, cat.NeedsLongContextBeta(m))
	if custom && m.MaxOutput > 0 && !countTokens {
		// Claude Code caps an unknown model at 32k output tokens; thinking
		// at xhigh/max needs the model's real ceiling.
		var mt int
		if json.Unmarshal(fields["max_tokens"], &mt) == nil && mt > 0 && mt < m.MaxOutput {
			fields["max_tokens"], _ = json.Marshal(m.MaxOutput)
		}
	}
	if custom && rt.scope == catalog.ScopeMain && p.Cfg.Features.PerTurnEffort && perTurn(cat, m) {
		// The plain body (top-level effort only) is kept for the retry
		// transport in case the API refuses per-turn effort.
		plain := cloneFields(fields)
		applyEffort(plain, m, dec.Effort, countTokens)
		if base, ok := p.turnEffort(rt.sessionID, fields, dec.Effort, countTokens); ok {
			applyEffort(fields, m, base, countTokens)
			rt.plainBody, _ = json.Marshal(plain)
			rt.perTurnBeta = cat.Meta.PerTurnEffortBeta
			setBeta(r.Header, rt.perTurnBeta, true)
		} else {
			fields = plain
		}
	} else {
		applyEffort(fields, m, dec.Effort, countTokens)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	if p.Debug {
		log.Printf("%s %s session=%s agent=%s tier=%s model=%s effort=%s", r.Method, r.URL.Path, short(rt.sessionID), rt.agentID, dec.Tier, m.APIID, dec.Effort)
	}
	return out, true
}

// decisionFor resolves the tier of a request for the routed model.
func (p *Proxy) decisionFor(cat *catalog.Catalog, rt *route, fields map[string]json.RawMessage, size int) *state.Decision {
	var sess *state.Session
	if rt.sessionID != "" {
		sess, _ = p.State.Load(rt.sessionID)
	}
	if rt.agentID != "" && sess != nil {
		// Its own decision, even on a tier the catalog has since retired:
		// the agent keeps the model and effort it was given.
		if b := p.binding(rt.sessionID, rt.agentID, fields); b != nil && cat.Model(b.Model) != nil {
			return b
		}
	}
	// A model the user pinned outside the tiers applies as is.
	if sess != nil && sess.Main != nil && sess.Main.Tier == state.PinnedTier && cat.Model(sess.Main.Model) != nil {
		d := *sess.Main
		return &d
	}
	// Main thread, and subagents that inherit the session model (workflow
	// agents without an explicit model do), follow the main decision.
	if sess != nil && sess.Main != nil && cat.Tier(catalog.ScopeMain, sess.Main.Tier) != nil {
		d := *sess.Main
		t := cat.Tier(catalog.ScopeMain, d.Tier) // re-resolve: the catalog may have changed
		// A tier with a small window (max_context) can be outgrown within a
		// turn, between two routing decisions: the request then goes to
		// the next tier that fits. The body size (4 bytes a token, JSON
		// included) overestimates, which is the safe side.
		if n := max(sess.ContextTokens, size/4); !cat.Fits(t, n) {
			for _, up := range cat.TiersByRank(catalog.ScopeMain) {
				if up.Rank > t.Rank && cat.Fits(up, n) {
					log.Printf("session %s: ~%d tokens outgrow tier %s, sent as %s", short(rt.sessionID), n, t.ID, up.ID)
					t, d.Tier = up, up.ID
					break
				}
			}
		}
		d.Model, d.Effort, d.Workflows = cat.Resolve(t, d.Mode)
		return &d
	}
	t := cat.DefaultTier(catalog.ScopeMain)
	return &state.Decision{Scope: catalog.ScopeMain, Tier: t.ID, Model: t.Model, Effort: t.Effort, Trigger: "default"}
}

// applyEffort sets output_config.effort, or strips effort and adaptive
// thinking for models without effort support (they reject both with a 400).
func applyEffort(fields map[string]json.RawMessage, m *catalog.Model, effort string, countTokens bool) {
	oc := map[string]json.RawMessage{}
	if raw, ok := fields["output_config"]; ok {
		if json.Unmarshal(raw, &oc) != nil {
			return
		}
	}
	if len(m.Efforts) == 0 {
		delete(oc, "effort")
		var th struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(fields["thinking"], &th) == nil && th.Type == "adaptive" {
			delete(fields, "thinking")
			stripClearThinking(fields)
		}
	} else if effort != "" {
		if _, had := fields["output_config"]; !had && countTokens {
			return
		}
		oc["effort"], _ = json.Marshal(effort)
	}
	if len(oc) == 0 {
		delete(fields, "output_config")
		return
	}
	fields["output_config"], _ = json.Marshal(oc)
}

// setBeta adds or removes one anthropic-beta value. Claude Code asks for the
// long-context window only for model names ending in "[1m]", and sizes the
// routed model's window from CLAUDE_CODE_MAX_CONTEXT_TOKENS: without the beta
// the API would cap a long session at 200k. Smaller models reject it.
func setBeta(h http.Header, beta string, want bool) {
	if beta == "" {
		return
	}
	var vals []string
	has := false
	for _, v := range h.Values(HeaderBeta) {
		for _, b := range strings.Split(v, ",") {
			switch b = strings.TrimSpace(b); {
			case b == "":
			case b == beta:
				has = true
				if want {
					vals = append(vals, b)
				}
			default:
				vals = append(vals, b)
			}
		}
	}
	if has == want {
		return
	}
	if want {
		vals = append(vals, beta)
	}
	if len(vals) == 0 {
		h.Del(HeaderBeta)
		return
	}
	h.Set(HeaderBeta, strings.Join(vals, ","))
}

// stripClearThinking drops context-management edits that require thinking.
func stripClearThinking(fields map[string]json.RawMessage) {
	var cm map[string]json.RawMessage
	if json.Unmarshal(fields["context_management"], &cm) != nil {
		return
	}
	var edits []map[string]any
	if json.Unmarshal(cm["edits"], &edits) != nil {
		return
	}
	kept := edits[:0]
	for _, e := range edits {
		if t, _ := e["type"].(string); !strings.HasPrefix(t, "clear_thinking") {
			kept = append(kept, e)
		}
	}
	if len(kept) == 0 {
		delete(fields, "context_management")
		return
	}
	cm["edits"], _ = json.Marshal(kept)
	fields["context_management"], _ = json.Marshal(cm)
}

// sessionID: header, then metadata.user_id (a JSON string carrying
// session_id), then the prompt index written by the decide hook.
func (p *Proxy) sessionID(r *http.Request, fields map[string]json.RawMessage) string {
	if id := r.Header.Get(HeaderSession); id != "" {
		return id
	}
	var md struct {
		UserID string `json:"user_id"`
	}
	if json.Unmarshal(fields["metadata"], &md) == nil && md.UserID != "" {
		var uid struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(md.UserID), &uid) == nil && uid.SessionID != "" {
			return uid.SessionID
		}
		if i := strings.LastIndex(md.UserID, "_session_"); i >= 0 {
			return md.UserID[i+len("_session_"):]
		}
	}
	for _, text := range lastUserTexts(fields) {
		if id := p.State.LookupPrompt(text); id != "" {
			return id
		}
	}
	return ""
}

// binding returns the subagent decision bound to agentID, binding it on the
// first request whose opening message contains a pending Agent prompt. The
// bound decision is read from the session state on every request: the
// message hook decides again when the agent is sent a new message.
func (p *Proxy) binding(sessionID, agentID string, fields map[string]json.RawMessage) *state.Decision {
	if s, err := p.State.Load(sessionID); err == nil {
		if d, ok := s.Agents[agentID]; ok && d != nil {
			cp := *d
			return &cp
		}
	}
	p.mu.Lock()
	tried := p.tried[agentID]
	p.mu.Unlock()
	if tried {
		return nil // its first message matched no pending Agent call
	}
	var b *state.Decision
	first := state.NormalizePrompt(strings.Join(firstUserTexts(fields), "\n"))
	_, err := p.State.Update(sessionID, func(s *state.Session) bool {
		if d, ok := s.Agents[agentID]; ok {
			b = d
			return false
		}
		for i, pa := range s.PendingAgents {
			if pa.Prompt != "" && strings.Contains(first, pa.Prompt) {
				d := pa.Decision
				b = &d
				if s.Agents == nil {
					s.Agents = map[string]*state.Decision{}
				}
				s.Agents[agentID] = b
				s.PendingAgents = append(s.PendingAgents[:i], s.PendingAgents[i+1:]...)
				return true
			}
		}
		return false
	})
	if err != nil {
		log.Printf("binding %s: %v", agentID, err)
		return nil
	}
	p.mu.Lock()
	if len(p.tried) > maxBindings {
		p.tried = map[string]bool{}
	}
	p.tried[agentID] = true
	p.mu.Unlock()
	if b == nil {
		return nil
	}
	cp := *b
	return &cp
}

// touch applies a throttled state update for a session.
func (p *Proxy) touch(sessionID string, fn func(*state.Session)) {
	if sessionID == "" {
		return
	}
	p.mu.Lock()
	last := p.touched[sessionID]
	if time.Since(last) < stateThrottle {
		p.mu.Unlock()
		return
	}
	p.touched[sessionID] = time.Now()
	p.mu.Unlock()
	p.bg.Add(1)
	go func() {
		defer p.bg.Done()
		if _, err := p.State.Update(sessionID, func(s *state.Session) bool { fn(s); return true }); err != nil {
			log.Printf("state %s: %v", short(sessionID), err)
		}
	}()
}

func messages(fields map[string]json.RawMessage) []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
} {
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(fields["messages"], &msgs)
	return msgs
}

func texts(content json.RawMessage) []string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return []string{s}
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(content, &blocks)
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			out = append(out, b.Text)
		}
	}
	return out
}

func firstUserTexts(fields map[string]json.RawMessage) []string {
	for _, m := range messages(fields) {
		if m.Role == "user" {
			return texts(m.Content)
		}
	}
	return nil
}

func lastUserTexts(fields map[string]json.RawMessage) []string {
	msgs := messages(fields)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return texts(msgs[i].Content)
		}
	}
	return nil
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
