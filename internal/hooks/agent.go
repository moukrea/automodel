package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
)

// PendingPromptChars is how much of an Agent prompt the proxy matches
// against a new subagent's first message to bind its effort.
const PendingPromptChars = 400

// PendingTTL bounds how long an unbound registration is kept.
const PendingTTL = 30 * time.Minute

// Agent is the PreToolUse hook for the Agent tool. It picks the subagent
// tier with Jev and sets the per-invocation model alias. The Agent tool has
// no effort field, so the effort is registered for the proxy, which binds it
// to the subagent's X-Claude-Code-Agent-Id on its first request.
func Agent(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	if in.ToolName != "Agent" && in.ToolName != "Task" {
		return nil, nil
	}
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	mi := detectModel(env, sess, in, transcriptIfUnknown(sess, in))
	if !env.IsCustom(mi.Model) {
		return nil, nil
	}
	var input map[string]any
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return nil, fmt.Errorf("tool_input: %w", err)
	}
	if m, _ := input["model"].(string); m != "" && env.Cfg.RespectExplicitSubagentModel {
		return nil, nil
	}
	prompt, _ := input["prompt"].(string)
	agentType, _ := input["subagent_type"].(string)
	description, _ := input["description"].(string)

	budget := env.Budget(catalog.ScopeSubagent)
	st := map[string]any{
		"task":          tokens.Truncate(prompt, budget-500),
		"subagent_type": agentType,
		"description":   description,
	}
	if agentType == "" {
		st["subagent_type"] = "general-purpose"
	}
	if sess.Main != nil {
		st["parent_tier"] = sess.Main.Tier
	}
	if sess.Repo != nil && len(sess.Repo.Languages) > 0 {
		st["repo_languages"] = sess.Repo.Languages
	}
	dec, _ := env.Decide(ctx, router.Request{SessionID: in.SessionID, Scope: catalog.ScopeSubagent,
		Trigger: "agent", AgentType: agentType, State: st, RepoDir: in.Cwd})

	model := env.Catalog.Model(dec.Model)
	input["model"] = model.Alias

	now := env.Now()
	if _, err := env.State.Update(in.SessionID, func(s *state.Session) bool {
		kept := s.PendingAgents[:0]
		for _, p := range s.PendingAgents {
			if now.Sub(p.CreatedAt) < PendingTTL {
				kept = append(kept, p)
			}
		}
		s.PendingAgents = append(kept, state.PendingAgent{
			Prompt: head(state.NormalizePrompt(prompt), PendingPromptChars), Decision: *dec, CreatedAt: now,
		})
		return true
	}); err != nil {
		return nil, err
	}
	return &Output{HookSpecificOutput: &Specific{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "allow",
		PermissionDecisionReason: fmt.Sprintf("automodel: subagent tier %s", dec.Tier),
		UpdatedInput:             input,
	}}, nil
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
