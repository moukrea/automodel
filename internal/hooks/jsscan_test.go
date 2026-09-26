package hooks

import "testing"

const script = "export const meta = { name: 'x', description: 'agent(ignored)', phases: [{ title: 'A' }] }\n" +
	"// agent('in a comment')\n" +
	"const s = \"agent('in a string')\"\n" +
	"const r = await agent(`Review ${files.map(f => `${f}`).join(', ')} for bugs`, {label: 'review', schema: S})\n" +
	"const q = await agent('plain prompt')\n" +
	"const e = await agent('explicit', { model: 'haiku' })\n" +
	"const o = await agent(p, opts)\n" +
	"obj.agent('method call')\n" +
	"await parallel(xs.map(x => () => agent(`fix ${x}`, {phase: 'Fix'})))\n"

func TestFindAgentCalls(t *testing.T) {
	sites, err := findAgentCalls(script)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"`Review ${files.map(f => `${f}`).join(', ')} for bugs`", "{label: 'review', schema: S}"},
		{"'plain prompt'"},
		{"'explicit'", "{ model: 'haiku' }"},
		{"p", "opts"},
		{"`fix ${x}`", "{phase: 'Fix'}"},
	}
	if len(sites) != len(want) {
		t.Fatalf("got %d sites: %+v", len(sites), sites)
	}
	for i, s := range sites {
		if len(s.Args) != len(want[i]) {
			t.Errorf("site %d: args %q", i, s.Args)
			continue
		}
		for j := range s.Args {
			if s.Args[j] != want[i][j] {
				t.Errorf("site %d arg %d: %q, want %q", i, j, s.Args[j], want[i][j])
			}
		}
		if got := script[s.Start:s.End]; got == "" {
			t.Errorf("site %d: empty span", i)
		}
	}
	if !hasKey("{ model: 'haiku' }", "model") || hasKey("{label: 'x', schema: {model: 1}}", "model") || !hasKey("{model}", "model") {
		t.Error("hasKey")
	}
}

func TestFindAgentCallsUnbalanced(t *testing.T) {
	if _, err := findAgentCalls("await agent(`unterminated"); err == nil {
		t.Error("want error")
	}
}
