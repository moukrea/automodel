package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/tokens"
)

// maxWorkflowSites bounds the Jev calls per workflow script.
const maxWorkflowSites = 24

// Workflow is the PreToolUse hook for the Workflow tool (spec §5.3, phase 2).
// For each agent(...) call site of an inline script that doesn't name a
// model, it asks Jev for a subagent tier and injects {model, effort} into the
// call's options. The injected keys come first and the original options are
// spread after them, so anything the script sets explicitly still wins.
// Saved workflows (name) and script files (scriptPath) are left alone: a
// resumed run re-reads the persisted script, which already carries the tiers.
func Workflow(ctx context.Context, env *router.Env, in *Input) (*Output, error) {
	if in.ToolName != "Workflow" || !env.Cfg.RouteWorkflowSteps {
		return nil, nil
	}
	sess, err := env.State.Load(in.SessionID)
	if err != nil {
		return nil, err
	}
	if !env.IsCustom(detectModel(env, sess, in, transcriptIfUnknown(sess, in)).Model) {
		return nil, nil
	}
	var input map[string]any
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return nil, fmt.Errorf("tool_input: %w", err)
	}
	script, _ := input["script"].(string)
	if script == "" {
		return nil, nil
	}
	sites, err := findAgentCalls(script)
	if err != nil {
		log.Printf("workflow: %v; script left unchanged", err)
		return nil, nil
	}
	var todo []int
	for i, s := range sites {
		if len(s.Args) == 0 || len(s.Args) > 2 {
			continue
		}
		if len(s.Args) == 2 && (hasKey(s.Args[1], "model") || hasKey(s.Args[1], "effort")) &&
			env.Cfg.RespectExplicitSubagentModel {
			continue
		}
		todo = append(todo, i)
	}
	if len(todo) == 0 {
		return nil, nil
	}
	if len(todo) > maxWorkflowSites {
		log.Printf("workflow: %d call sites, routing the first %d", len(todo), maxWorkflowSites)
		todo = todo[:maxWorkflowSites]
	}

	budget := env.Budget(catalog.ScopeSubagent)
	decisions := make([]*state.Decision, len(sites))
	var wg sync.WaitGroup
	for _, i := range todo {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := sites[i]
			st := map[string]any{
				"task":          tokens.Truncate("Workflow stage prompt (JavaScript source):\n"+s.Args[0], budget-800),
				"subagent_type": "workflow-agent",
			}
			if len(s.Args) == 2 {
				st["stage_options"] = tokens.Truncate(s.Args[1], 300)
			}
			if sess.Main != nil {
				st["parent_tier"] = sess.Main.Tier
			}
			label := ""
			if len(s.Args) == 2 {
				label = stageLabel(s.Args[1])
			}
			decisions[i], _ = env.Decide(ctx, router.Request{SessionID: in.SessionID, Scope: catalog.ScopeSubagent,
				Trigger: "workflow", AgentType: "workflow", Label: label, State: st, RepoDir: in.Cwd})
		}(i)
	}
	wg.Wait()

	out := script
	var summary []string
	for i := len(sites) - 1; i >= 0; i-- {
		d := decisions[i]
		if d == nil || d.Trigger == "fallback" {
			continue // inherit the session model
		}
		s := sites[i]
		inject := fmt.Sprintf("model: %q", env.Catalog.Model(d.Model).Alias)
		if d.Effort != "" {
			inject += fmt.Sprintf(", effort: %q", d.Effort)
		}
		var args string
		if len(s.Args) == 2 {
			args = fmt.Sprintf("%s, {%s, ...(%s)}", s.Args[0], inject, s.Args[1])
		} else {
			args = fmt.Sprintf("%s, {%s}", s.Args[0], inject)
		}
		out = out[:s.Start] + args + out[s.End:]
		summary = append(summary, d.Tier)
	}
	if out == script {
		return nil, nil
	}
	input["script"] = out
	return &Output{HookSpecificOutput: &Specific{
		HookEventName:            "PreToolUse",
		PermissionDecision:       "allow",
		PermissionDecisionReason: "automodel: workflow stage tiers " + strings.Join(reverse(summary), ", "),
		UpdatedInput:             input,
	}}, nil
}

func reverse(xs []string) []string {
	for i, j := 0, len(xs)-1; i < j; i, j = i+1, j-1 {
		xs[i], xs[j] = xs[j], xs[i]
	}
	return xs
}

var stageLabelRE = regexp.MustCompile("\\b(label|phase)\\s*:\\s*['\"`]([^'\"`$]{1,60})['\"`]")

// stageLabel returns an agent() call's label (else its phase), for the ledger.
func stageLabel(opts string) string {
	var phase string
	for _, m := range stageLabelRE.FindAllStringSubmatch(opts, -1) {
		if m[1] == "label" {
			return m[2]
		}
		phase = m[2]
	}
	return phase
}
