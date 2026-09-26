// Package repo computes the repository signals sent to Jev (languages, size,
// pending diff, CLAUDE.md head). Computed once per session and cached.
package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/state"
)

var languages = map[string]string{
	".go": "Go", ".rs": "Rust", ".py": "Python", ".ts": "TypeScript", ".tsx": "TypeScript",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".java": "Java", ".kt": "Kotlin",
	".swift": "Swift", ".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".hpp": "C++",
	".cs": "C#", ".rb": "Ruby", ".php": "PHP", ".scala": "Scala", ".ex": "Elixir", ".exs": "Elixir",
	".erl": "Erlang", ".hs": "Haskell", ".ml": "OCaml", ".clj": "Clojure", ".lua": "Lua",
	".sh": "Shell", ".bash": "Shell", ".zig": "Zig", ".dart": "Dart", ".vue": "Vue", ".svelte": "Svelte",
	".sql": "SQL", ".tf": "Terraform", ".nix": "Nix", ".proto": "Protobuf", ".r": "R", ".jl": "Julia",
}

const (
	maxWalk     = 20000
	claudeMDMax = 6000
	diffStatMax = 4000
)

// Signals computes the signals for dir within the deadline of ctx.
func Signals(ctx context.Context, dir string) *state.RepoSignals {
	ctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
	defer cancel()
	s := &state.RepoSignals{Root: dir}
	var files []string
	if root := git(ctx, dir, "rev-parse", "--show-toplevel"); root != "" {
		s.Root = root
		files = strings.Split(git(ctx, dir, "ls-files"), "\n")
		stat := git(ctx, dir, "diff", "HEAD", "--stat", "--stat-width=100")
		if stat == "" {
			stat = git(ctx, dir, "diff", "--stat", "--stat-width=100")
		}
		s.DiffStat = cut(stat, diffStatMax)
	} else {
		files = walk(dir)
	}
	counts := map[string]int{}
	for _, f := range files {
		if f == "" {
			continue
		}
		s.Files++
		if l, ok := languages[strings.ToLower(filepath.Ext(f))]; ok {
			counts[l]++
		}
	}
	for l := range counts {
		s.Languages = append(s.Languages, l)
	}
	sort.Slice(s.Languages, func(i, j int) bool {
		a, b := s.Languages[i], s.Languages[j]
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		return a < b
	})
	if len(s.Languages) > 6 {
		s.Languages = s.Languages[:6]
	}
	for _, name := range []string{"CLAUDE.md", ".claude/CLAUDE.md", "AGENTS.md"} {
		if b, err := os.ReadFile(filepath.Join(s.Root, name)); err == nil {
			s.ClaudeMDHead = cut(string(b), claudeMDMax)
			break
		}
	}
	return s
}

func git(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func walk(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if len(out) >= maxWalk {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if p != dir && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == "target") {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[…]"
}
