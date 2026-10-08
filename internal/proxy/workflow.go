package proxy

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
)

const (
	// HeaderAgentType names the kind of agent a request comes from.
	HeaderAgentType = "X-Claude-Code-Agent-Type"
	// AgentTypeWorkflow is the agent type of a Workflow tool's agent() call.
	AgentTypeWorkflow = "workflow-subagent"
	// TriggerWorkflowAgent marks a subagent decision the proxy took for a
	// workflow agent that asked for the session's model.
	TriggerWorkflowAgent = "workflow_agent"
)

// Decider takes a routing decision on the catalog in use (router.Env.Decide).
type Decider func(ctx context.Context, cat *catalog.Catalog, req router.Request) *state.Decision

// workflowAgent gives a workflow agent that asks for the session's model its
// own subagent decision, from the prompt it was actually sent: the workflow
// hook routes the stages it can read, but a stage built from the script's
// data, a saved or resumed run (scriptPath) or a hook that didn't run leave
// the agent on the main thread's model and effort. The decision is bound to
// the agent, so its later requests keep it (its cache stays on one model).
// A pinned session, or a model the owner named for the work, is left alone.
func (p *Proxy) workflowAgent(ctx context.Context, cat *catalog.Catalog, rt *route, fields map[string]json.RawMessage) {
	if p.Decide == nil || !p.Cfg.Features.WorkflowAgentsOwnLevel || rt.sessionID == "" {
		return
	}
	sess, err := p.State.Load(rt.sessionID)
	if err != nil || sess == nil {
		return
	}
	if _, ok := sess.Agents[rt.agentID]; ok {
		return
	}
	if sess.PinModel != "" || (sess.Work != nil && sess.Work.Model != "") ||
		(sess.Main != nil && sess.Main.Tier == state.PinnedTier) {
		return
	}
	task := stageTask(firstUserTexts(fields))
	if task == "" {
		return
	}
	// One decision per agent, even when its first requests run in parallel.
	p.mu.Lock()
	if p.wfDeciding == nil {
		p.wfDeciding = map[string]chan struct{}{}
	}
	if ch, ok := p.wfDeciding[rt.agentID]; ok {
		p.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		return
	}
	ch := make(chan struct{})
	p.wfDeciding[rt.agentID] = ch
	if len(p.wfDeciding) > maxBindings {
		p.wfDeciding = map[string]chan struct{}{rt.agentID: ch}
	}
	p.mu.Unlock()
	defer close(ch)

	budget := 24000
	if p.Cfg.StateBudgetTokens > 0 {
		budget = p.Cfg.StateBudgetTokens
	}
	st := map[string]any{
		"task":          tokens.Truncate("Workflow stage prompt:\n"+task, budget-800),
		"subagent_type": "workflow-agent",
	}
	if sess.Main != nil {
		st["parent_tier"] = sess.Main.Tier
	}
	req := router.Request{SessionID: rt.sessionID, Scope: catalog.ScopeSubagent, Trigger: "workflow",
		AgentType: "workflow", AgentID: rt.agentID, State: st}
	if sess.Repo != nil {
		req.RepoDir, req.RepoRoot = sess.Repo.Root, sess.Repo.Root
	}
	d := p.Decide(ctx, cat, req)
	if d == nil || d.Trigger == "fallback" || cat.Model(d.Model) == nil {
		return // keeps the session's model
	}
	// Claude Code sizes the agent's context for the custom model, not for
	// the model it gets: a tier on a smaller window than the main thread's
	// would overflow before Claude Code compacts. Take the next one that has it.
	if t := cat.Tier(catalog.ScopeSubagent, d.Tier); t != nil && !cat.Fits(t, cat.Meta.MinMainContext()) {
		for _, up := range cat.TiersByRank(catalog.ScopeSubagent) {
			if up.Rank > t.Rank && cat.Fits(up, cat.Meta.MinMainContext()) {
				d.Tier = up.ID
				d.Model, d.Effort, _ = cat.Resolve(up, "")
				break
			}
		}
		if d.Tier == t.ID {
			return
		}
	}
	m := cat.Model(d.Model)
	d.APIID, d.Trigger = m.APIID, TriggerWorkflowAgent
	_, err = p.State.Update(rt.sessionID, func(s *state.Session) bool {
		if _, ok := s.Agents[rt.agentID]; ok {
			return false
		}
		if s.Agents == nil {
			s.Agents = map[string]*state.Decision{}
		}
		s.Agents[rt.agentID] = d
		return true
	})
	if err != nil {
		log.Printf("workflow agent %s: %v", rt.agentID, err)
		return
	}
	if p.Debug {
		log.Printf("workflow agent %s session=%s: %s (%s %s)", rt.agentID, short(rt.sessionID), d.Tier, m.APIID, d.Effort)
	}
}

const (
	harnessTask    = "[Workflow harness — computed task]"
	harnessRequest = "[Workflow harness — user request]"
)

// stageTask returns the prompt a workflow agent was given: the computed
// task the harness frames (its preamble dropped, its lines unindented),
// without the system reminders nor the relayed user request (the main
// thread's prompt, which every stage of the run carries).
func stageTask(texts []string) string {
	var rest []string
	for _, t := range texts {
		t = strings.TrimSpace(t)
		switch {
		case strings.HasPrefix(t, harnessTask):
			_, body, _ := strings.Cut(t, "\n")
			lines := strings.Split(body, "\n")
			for i, l := range lines {
				lines[i] = strings.TrimPrefix(l, "  ")
			}
			return strings.TrimSpace(strings.Join(lines, "\n"))
		case t == "", strings.HasPrefix(t, "<system-reminder>"), strings.HasPrefix(t, harnessRequest):
		default:
			rest = append(rest, t)
		}
	}
	return strings.Join(rest, "\n\n")
}
