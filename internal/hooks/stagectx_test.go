package hooks

import (
	"strings"
	"testing"
)

func TestLiteralChars(t *testing.T) {
	for src, want := range map[string]int{
		"`${COMMON}\\n\\n${r.prompt}`": 2, // an escape counts once in a template
		"BASE + '\\n\\n' + d.p":        4,
		"t.prompt":                     0,
		"'list files'":                 10,
		"`port ${m.port ? `\\nport: ${m.port}` : ''}`": 12,
	} {
		if got := literalChars(src); got != want {
			t.Errorf("literalChars(%s) = %d, want %d", src, got, want)
		}
	}
}

func TestRefs(t *testing.T) {
	got := refs("`${COMMON}\\n\\n${m.task}${m.port ? `\\nport: ${m.port}.` : ''}` + JSON.stringify(x.y) + VERIFY(m, impl)")
	if strings.Join(got, ",") != "COMMON,m,x,VERIFY,impl" {
		t.Errorf("refs = %v", got)
	}
}

const stageScript = `export const meta = {name: 'x'}
const BASE = ` + "`Repo rules: read AGENTS.md first.`" + `
const DIMS = [
  { key: 'races', p: 'Hunt for data races in the scheduler.' },
  { key: 'leaks', p: 'Find leaked file handles on error paths.' },
]
// const DIMS = 'a comment, not a declaration'
function brief(d) {
  return BASE + d.p
}
const first = DIMS.filter(d => d.key !== 'leaks')
const long = 'one' +
  ' two'
const r = await pipeline(DIMS, d => agent(BASE + '\n\n' + d.p, {label: 'review:' + d.key}))
await parallel(first.map(m => () => agent(brief(m))))
`

func TestDeclaration(t *testing.T) {
	if d := declaration(stageScript, "DIMS"); !strings.HasPrefix(d, "[") || !strings.HasSuffix(d, "]") || !strings.Contains(d, "leaks") {
		t.Errorf("DIMS = %q", d)
	}
	if d := declaration(stageScript, "long"); d != "'one' +\n  ' two'" {
		t.Errorf("a continued initializer: %q", d)
	}
	if d := declaration(stageScript, "brief"); !strings.HasPrefix(d, "function brief(d) {") || !strings.HasSuffix(d, "}") {
		t.Errorf("a function: %q", d)
	}
	if d := declaration(stageScript, "nope"); d != "" {
		t.Errorf("undeclared: %q", d)
	}
}

func TestStageContext(t *testing.T) {
	sites, err := findAgentCalls(stageScript)
	if err != nil || len(sites) != 2 {
		t.Fatalf("sites %v, %v", sites, err)
	}
	ctx := stageContext(stageScript, sites[0], maxStageContext)
	if !strings.Contains(ctx, "d ranges over DIMS") || !strings.Contains(ctx, "Hunt for data races") || !strings.Contains(ctx, "BASE = `Repo rules") {
		t.Errorf("pipeline stage context:\n%s", ctx)
	}
	// A loop over an array derived from another one ranges over the latter;
	// a prompt builder is shown with it.
	ctx = stageContext(stageScript, sites[1], maxStageContext)
	if !strings.Contains(ctx, "m ranges over DIMS") || !strings.Contains(ctx, "function brief") {
		t.Errorf("derived loop context:\n%s", ctx)
	}
	// Shared context is cut short; the task keeps its room.
	big := strings.Replace(stageScript, "Repo rules: read AGENTS.md first.", strings.Repeat("rules ", 400), 1)
	sites, _ = findAgentCalls(big)
	ctx = stageContext(big, sites[0], maxStageContext)
	if strings.Count(ctx, "rules ") > sharedChars/6+1 || !strings.Contains(ctx, "Find leaked file handles") {
		t.Errorf("shared context not cut, or task lost (%d chars)", len(ctx))
	}
}
