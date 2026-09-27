package router

import (
	"regexp"
	"sort"
	"strings"
)

// Privacy modes: what the routing state sent to Jev (OpenRouter) contains.
const (
	PrivacyFull     = "full"     // prompts, recent prompts, last reply, compaction summary, repo excerpts
	PrivacyMetadata = "metadata" // no text: sizes, task-kind hints, repo languages and size
)

// kindHints are words that say what kind of work a prompt asks for (English
// and French); in metadata mode they stand in for the text.
var kindHints = map[string][]string{
	"question":    {"what", "why", "how", "explain", "quoi", "pourquoi", "comment", "explique"},
	"bug":         {"bug", "fix", "broken", "error", "crash", "fails", "failing", "corrige", "erreur", "plante"},
	"concurrency": {"race", "concurrent", "concurrency", "deadlock", "lock", "thread"},
	"security":    {"security", "vulnerab", "injection", "auth", "sécurité", "securite"},
	"refactor":    {"refactor", "rename", "cleanup", "clean up", "restructure", "renomme"},
	"test":        {"test", "tests", "coverage"},
	"review":      {"review", "audit", "revue"},
	"docs":        {"doc", "readme", "comment", "documentation"},
	"commit":      {"commit message", "changelog", "pr description"},
	"design":      {"design", "architecture", "plan", "trade-off", "tradeoff", "conception"},
	"performance": {"performance", "slow", "optimi", "latency", "lent"},
	"migration":   {"migrate", "migration", "upgrade", "port "},
	"parallel":    {"entire repo", "whole codebase", "all files", "every file", "tout le repo"},
}

var codeRE = regexp.MustCompile("```|\\bfunc |\\bdef |\\bclass |=>|;\\s*$")

// textFeatures describes a text without its content.
func textFeatures(s string) map[string]any {
	low := strings.ToLower(s)
	var hints []string
	for kind, words := range kindHints {
		for _, w := range words {
			if strings.Contains(low, w) {
				hints = append(hints, kind)
				break
			}
		}
	}
	f := map[string]any{"words": len(strings.Fields(s)), "chars": len(s)}
	if len(hints) > 0 {
		sort.Strings(hints)
		f["kind_hints"] = hints
	}
	if codeRE.MatchString(s) {
		f["has_code"] = true
	}
	if strings.Contains(s, "?") {
		f["asks_question"] = true
	}
	return f
}

// MetadataOnly returns the routing state without any text written by the
// user, the assistant or the repository.
func MetadataOnly(st map[string]any) map[string]any {
	out := map[string]any{"privacy": "metadata: text replaced by features"}
	for k, v := range st {
		switch k {
		case "task", "compaction_summary", "last_assistant", "stage_options", "description":
			if s, ok := v.(string); ok {
				out[k+"_features"] = textFeatures(s)
			}
		case "recent_prompts":
			if l, ok := v.([]string); ok {
				fs := make([]map[string]any, len(l))
				for i, p := range l {
					fs[i] = textFeatures(p)
				}
				out["recent_prompts_features"] = fs
			}
		case "repo":
			if r, ok := v.(map[string]any); ok {
				out["repo"] = map[string]any{"languages": r["languages"], "files": r["files"]}
			}
		default: // phase, current tier, session sizes, subagent type, parent tier, languages
			out[k] = v
		}
	}
	return out
}
