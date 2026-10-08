package hooks

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
)

// TriggerResumeAgent is the trigger of a subagent decision taken when the
// agent is sent a new message: the proxy applies its model even to a request
// that names another one.
const TriggerResumeAgent = "resume_agent"

// Message is the PreToolUse hook for SendMessage. A subagent continued with
// a message gets its own subagent decision for that message, instead of the
// level of whoever spawned it: an agent resumed after Claude Code restarted
// asks for the session's model, so it followed the main decision (live: one
// agent ran Opus xhigh for two days on rebases and CI checks, $441). The
// agent's cache is warm on its current model: a switch rewrites the whole
// context, so it is weighed like a warm main-session switch. Forks keep the
// parent's model (its cache), and so does an agent on a model the user asked
// for.
func Message(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	if in.ToolName != "SendMessage" || !env.Cfg.Features.ResumedAgentsOwnLevel {
		return nil, nil
	}
	var input struct {
		To      string `json:"to"`
		Message any    `json:"message"`
	}
	if json.Unmarshal(in.ToolInput, &input) != nil {
		return nil, nil
	}
	msg, ok := input.Message.(string)
	if !ok || strings.TrimSpace(msg) == "" {
		return nil, nil // a structured message (shutdown, plan approval)
	}
	ag := findAgent(in.TranscriptPath, in.SessionID, input.To)
	if ag == nil || (ag.Type == "fork" && env.Cfg.Features.ForksInherit) {
		return nil, nil
	}
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	if mi := detectModel(env, sess, in, transcriptIfUnknown(sess, in)); !env.IsCustom(mi.Model) {
		return nil, nil
	}
	// A model the user asked for, for the session or its work, holds.
	if sess.PinModel != "" || (sess.Work != nil && sess.Work.Model != "") {
		return nil, nil
	}
	c := env.Catalog
	cur := agentCurrent(env, sess, ag)
	if cur == nil {
		return nil, nil
	}
	budget := env.Budget(catalog.ScopeSubagent)
	st := map[string]any{
		"task":          tokens.Truncate(msg, budget-500),
		"subagent_type": ag.Type,
		"description":   ag.Description,
	}
	if ag.Type == "" {
		st["subagent_type"] = "general-purpose"
	}
	if sess.Main != nil {
		st["parent_tier"] = sess.Main.Tier
	}
	if sess.Repo != nil && len(sess.Repo.Languages) > 0 {
		st["repo_languages"] = sess.Repo.Languages
	}
	req := router.Request{SessionID: in.SessionID, Scope: catalog.ScopeSubagent, Trigger: TriggerResumeAgent,
		AgentType: ag.Type, AgentID: ag.ID, State: st, RepoDir: in.Cwd, Context: ag.Context}
	// Its current level, as the subagent tier on the same model nearest in
	// effort (an agent following the main decision runs a main tier).
	if t := nearestTier(c, cur.Model, cur.Effort); t != nil && ag.Warm(env.Now(), subagentTTL(env)) {
		d := *cur
		d.Scope, d.Tier = catalog.ScopeSubagent, t.ID
		req.Warm, req.Current = true, &d
		req.SwitchCost = agentSwitchCost(env, cur, ag.Context)
		req.Scale = agentScale(env, t, ag.TurnUSD)
	}
	dec, _ := env.Decide(ctx, req)
	if dec == nil || c.Model(dec.Model) == nil {
		return nil, nil
	}
	d := *dec
	d.Trigger = TriggerResumeAgent
	if m := c.Model(d.Model); d.APIID == "" {
		d.APIID = m.APIID
	}
	_, err = env.State.Update(in.SessionID, func(s *state.Session) bool {
		if s.Agents == nil {
			s.Agents = map[string]*state.Decision{}
		}
		s.Agents[ag.ID] = &d
		return true
	})
	return nil, err
}

// agentInfo is what the hook knows of a subagent from its transcript.
type agentInfo struct {
	ID, Type, Description, Name string
	Model                       string // API model of its last answer ("" if none)
	Context                     int    // prompt tokens of its last request
	Last                        time.Time
	TurnUSD                     float64 // observed cost per turn (0: unknown)
}

// Warm reports whether the agent's prompt cache is likely still there.
func (a *agentInfo) Warm(now time.Time, ttl time.Duration) bool {
	return !a.Last.IsZero() && now.Sub(a.Last) < ttl && a.Context > 0
}

var agentIDRe = regexp.MustCompile(`^a[0-9a-f]{8,}$`)

// findAgent resolves a SendMessage recipient (an agent's name or ID) to the
// subagent's transcript in the session's directory.
func findAgent(transcriptPath, sessionID, to string) *agentInfo {
	if to == "" || to == "*" || to == "main" || transcriptPath == "" {
		return nil
	}
	dir := filepath.Join(filepath.Dir(transcriptPath), sessionID, "subagents")
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	for _, p := range metas {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "agent-"), ".meta.json")
		var meta struct {
			AgentType   string `json:"agentType"`
			Description string `json:"description"`
			Name        string `json:"name"`
		}
		b, err := os.ReadFile(p)
		if err != nil || json.Unmarshal(b, &meta) != nil {
			continue
		}
		if to != id && to != meta.Name && !(agentIDRe.MatchString(to) && strings.HasPrefix(id, to)) {
			continue
		}
		a := &agentInfo{ID: id, Type: meta.AgentType, Description: meta.Description, Name: meta.Name}
		readAgentTranscript(strings.TrimSuffix(p, ".meta.json")+".jsonl", a)
		return a
	}
	return nil
}

// readAgentTranscript fills the agent's last model, context size and time,
// and its cost per turn (a message to it and the requests that answer it) at
// Opus 5.5 list prices: only a scale, compared with the catalog's relative
// costs of its current tier.
func readAgentTranscript(path string, a *agentInfo) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var turns int
	var tokensSeen float64
	seen := map[string]bool{} // one line per content block, same usage
	for sc.Scan() {
		line := sc.Bytes()
		var e struct {
			Type      string    `json:"type"`
			Timestamp time.Time `json:"timestamp"`
			Message   struct {
				ID      string          `json:"id"`
				Model   string          `json:"model"`
				Content json.RawMessage `json:"content"`
				Usage   *struct {
					Input       int `json:"input_tokens"`
					Output      int `json:"output_tokens"`
					CacheRead   int `json:"cache_read_input_tokens"`
					CacheCreate int `json:"cache_creation_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "user":
			if userText(e.Message.Content) {
				turns++
			}
		case "assistant":
			u := e.Message.Usage
			if u == nil || e.Message.Model == "<synthetic>" || (e.Message.ID != "" && seen[e.Message.ID]) {
				continue
			}
			seen[e.Message.ID] = true
			a.Model, a.Last = e.Message.Model, e.Timestamp
			a.Context = u.Input + u.CacheRead + u.CacheCreate
			// Opus 5.5 list prices: a scale for the agent's own cost.
			tokensSeen += (4*float64(u.Input) + 20*float64(u.Output) + 8*float64(u.CacheCreate) + 0.2*float64(u.CacheRead)) / 1e6
		}
	}
	if turns > 0 {
		a.TurnUSD = tokensSeen / float64(turns)
	}
}

// userText reports whether a user entry carries text (a message to the
// agent), not only tool results.
func userText(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == "text" {
			return true
		}
	}
	return false
}

// agentCurrent is the decision the agent runs at now: its own, else the main
// decision it follows when it asks for the session's model, else its model
// as it answered (effort unknown).
func agentCurrent(env *router.Env, sess *state.Session, ag *agentInfo) *state.Decision {
	c := env.Catalog
	if d, ok := sess.Agents[ag.ID]; ok && d != nil && c.Model(d.Model) != nil {
		cp := *d
		return &cp
	}
	if ag.Model == "" || env.IsCustom(ag.Model) {
		if sess.Main != nil && c.Model(sess.Main.Model) != nil {
			cp := *sess.Main
			cp.Mode, cp.Workflows = "", false // the main thread's, not the agent's
			return &cp
		}
		return nil
	}
	if m := c.ModelByAPIID(ag.Model); m != nil {
		return &state.Decision{Scope: catalog.ScopeSubagent, Model: m.ID, APIID: m.APIID}
	}
	return nil
}

var effortOrder = map[string]int{"low": 0, "medium": 1, "high": 2, "xhigh": 3, "max": 4}

// nearestTier is the subagent tier on model whose effort is nearest effort
// (the higher one on a tie), or nil.
func nearestTier(c *catalog.Catalog, model, effort string) *catalog.Tier {
	var best *catalog.Tier
	bestD := 99
	for _, t := range c.TiersByRank(catalog.ScopeSubagent) {
		if t.Model != model {
			continue
		}
		d := 0
		if effort != "" {
			d = effortOrder[t.Effort] - effortOrder[effort]
			if d < 0 {
				d = -d*2 + 1 // below costs more than above: a tie goes up
			} else {
				d *= 2
			}
		}
		if d < bestD {
			best, bestD = t, d
		}
	}
	return best
}

// agentSwitchCost: moving the agent off its model, or changing its effort
// (subagents get a top-level effort, part of the cached prefix), writes its
// whole context to the cache again instead of reading it.
func agentSwitchCost(env *router.Env, cur *state.Decision, context int) func(*catalog.Tier) float64 {
	c := env.Catalog
	from := c.Model(cur.Model)
	long := subagentTTL(env) >= time.Hour
	return func(t *catalog.Tier) float64 {
		if t.Model == cur.Model && (cur.Effort == "" || t.Effort == cur.Effort) {
			return 0
		}
		m := c.Model(t.Model)
		if m == nil || m.Price == nil || from == nil || from.Price == nil {
			return 0
		}
		write := m.Price.CacheWrite5m
		if long && m.Price.CacheWrite1h > 0 {
			write = m.Price.CacheWrite1h
		}
		return float64(context) * (write - from.Price.CacheRead) / 1e6
	}
}

// agentScale converts catalog cost units into dollars for the agent's next
// turn: its observed cost per turn relative to its current tier's catalog
// cost (one turn: a switch must pay back on the work just sent).
func agentScale(env *router.Env, t *catalog.Tier, turnUSD float64) float64 {
	if turnUSD <= 0 {
		return 1
	}
	if c := policyCost(env, t); c > 0 {
		return turnUSD / c
	}
	return 1
}

func policyCost(env *router.Env, t *catalog.Tier) float64 {
	return policy.Costs(env.Catalog, catalog.ScopeSubagent)[t.ID]
}

func subagentTTL(env *router.Env) time.Duration {
	if d, err := time.ParseDuration(env.Cfg.Features.SubagentCacheTTL); err == nil && d > 0 {
		return d
	}
	return 5 * time.Minute
}
