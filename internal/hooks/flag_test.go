package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moukrea/automodel/internal/eval"
	"github.com/moukrea/automodel/internal/ledger"
)

// A flagged decision becomes an eval case with the state that was sent:
// metadata only in a metadata repo.
func TestFlaggedDecisionIsAnEvalCase(t *testing.T) {
	fj := &fakeJev{answers: []fa{answer("low", 0.9)}}
	env := setup(t, fj)
	env.States = ledger.States{Dir: env.Cfg.StateDir}
	markJev(t, env, "s1")
	cwd := t.TempDir()
	os.MkdirAll(filepath.Join(cwd, ".git"), 0o755)
	os.WriteFile(filepath.Join(cwd, ".automodel.toml"), []byte("privacy = \"metadata\"\n"), 0o644)
	run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": "Fix the race in the scheduler", "cwd": cwd})

	all, err := ledger.Decisions(env.Cfg.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := ledger.Nth(all, "", "main", 1)
	if !ok || d.ID == "" || d.Repo != cwd {
		t.Fatalf("decision = %+v", d)
	}
	st, err := env.States.Get("s1", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.State["task"]; ok || st.State["task_features"] == nil {
		t.Fatalf("kept state is not what was sent: %v", st.State)
	}
	path := filepath.Join(t.TempDir(), "flagged.jsonl")
	if err := eval.AppendCase(path, eval.FromDecision(d, st, "xhigh", "too hard for low")); err != nil {
		t.Fatal(err)
	}
	cs, err := eval.Load(path)
	if err != nil || len(cs) != 1 {
		t.Fatalf("cases %v, %v", cs, err)
	}
	c := cs[0]
	if c.Want != "xhigh" || c.Accept[0] != "xhigh" || c.Scope != "main" || c.State["phase"] != "initial" || c.Note == "" {
		t.Errorf("case = %+v", c)
	}
}
