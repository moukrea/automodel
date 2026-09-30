package hooks

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/jev"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
)

const workGoal = "Find why checkouts hand the same connection to two workers under load, and fix it"

// workSession sets up a warm session whose decision in force is tier and
// mode, and whose work in progress is work (at mode).
func workSession(t *testing.T, env *router.Env, sid, tier, mode, work string) {
	t.Helper()
	warmSession(t, env, sid, tier, 300_000)
	c := env.Catalog
	_, effort, wf := c.Resolve(c.Tier(catalog.ScopeMain, tier), mode)
	env.State.Update(sid, func(s *state.Session) bool {
		s.Main.Effort, s.Main.Mode, s.Main.Workflows = effort, mode, wf
		s.UltracodeOn, s.UltracodeEpoch = wf, s.Main.Epoch
		s.Work = &state.Work{Tier: work, Mode: mode, Goal: workGoal, Since: time.Now().Add(-time.Hour)}
		return true
	})
}

// busyTranscript is a turn still running: the last message called a tool
// and no turn end followed.
func busyTranscript(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "busy.jsonl")
	os.WriteFile(p, []byte(strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"Add the price column to the CSV export"}}`,
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"Edit","input":{}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
	}, "\n")+"\n"), 0o600)
	return p
}

// The work in progress (router.Judge): a follow-up keeps at least its tier
// and mode, separate work gets its own level, requests in words win, and a
// prompt typed mid-turn or sent by another session never lowers anything.
func TestWorkInProgress(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	busy := busyTranscript(t)
	x := func(kv ...any) map[string]float64 {
		m := map[string]float64{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1].(float64)
		}
		return m
	}
	for i, c := range []struct {
		name       string
		tier, mode string // the decision in force, and the work in progress's mode
		work       string // the work in progress's tier ("": the decision's)
		transcript string
		prompt     string
		jev        fa
		ask        string // an explicit-request question the prompt must carry
		// The decision, and the work in progress after the prompt (newGoal:
		// the prompt started it).
		want, wantMode         string
		wantWork, wantWorkMode string
		newGoal                bool
	}{
		{name: "an extension at xhigh stays at xhigh", tier: "xhigh", prompt: "also add a regression test for that interleaving",
			jev: fa{tier: "medium", conf: 0.85, rel: "extend", relP: 0.97}, want: "xhigh", wantWork: "xhigh"},
		{name: "a side question holds", tier: "xhigh", prompt: "is CI green yet?",
			jev: fa{tier: "low", conf: 0.95, rel: "side_question"}, want: "xhigh", wantWork: "xhigh"},
		{name: "a harder extension raises the work", tier: "medium", prompt: "and handle the retry storm when the pool is exhausted too",
			jev: fa{tier: "xhigh", conf: 0.8, rel: "extend"}, want: "xhigh", wantWork: "xhigh"},
		{name: "a wrap-up lowers, the work stays", tier: "xhigh", prompt: "write the commit message",
			jev: fa{tier: "low", conf: 0.95, rel: "wrap_up"}, want: "low", wantWork: "xhigh"},
		{name: "a new task lowers and starts new work", tier: "xhigh", prompt: "now rename the config loader to settings",
			jev: fa{tier: "low", conf: 0.9, rel: "new_task"}, want: "low", wantWork: "low", newGoal: true},
		{name: "a hard new task on a low session goes up", tier: "low", prompt: "Next: find why refunds are counted twice under load",
			jev: fa{tier: "xhigh", conf: 0.9, rel: "new_task"}, want: "xhigh", wantWork: "xhigh", newGoal: true},
		{name: "unsure between follow-up and separate: holds", tier: "high", prompt: "et le cas où le worker meurt ?",
			jev: fa{tier: "low", conf: 0.9, rel: "new_task", relP: 0.45}, want: "high", wantWork: "high"}, // new_task + wrap_up 0.56
		{name: "an effort asked in words raises", tier: "high", prompt: "fais la suite en xhigh", ask: "explicit_effort_xhigh",
			jev: fa{tier: "high", conf: 0.9, rel: "extend", x: x("effort_xhigh", 0.95)}, want: "xhigh", wantWork: "xhigh"},
		{name: "a mention of an effort doesn't", tier: "high", prompt: "pourquoi c'est resté en xhigh tout à l'heure ?", ask: "explicit_effort_xhigh",
			jev: fa{tier: "low", conf: 0.9, rel: "side_question", x: x("effort_xhigh", 0.04)}, want: "high", wantWork: "high"},
		{name: "passe en low lowers the work too", tier: "xhigh", prompt: "passe en low pour la suite, c'est mécanique", ask: "explicit_effort_low",
			jev: fa{tier: "medium", conf: 0.9, rel: "extend", x: x("effort_low", 0.93)}, want: "low", wantWork: "low"},
		{name: "ultrathink: xhigh at least, without a question", tier: "low", prompt: "ultrathink: are there other places we read the lease without the lock?",
			jev: fa{tier: "low", conf: 0.9, rel: "side_question"}, want: "xhigh", wantWork: "xhigh"},
		{name: "ultracode asked on a continuing turn turns it on", tier: "high", prompt: "continue, en ultracode cette fois", ask: "explicit_mode_ultracode",
			jev: fa{tier: "high", conf: 0.9, ultra: 0.3, rel: "continue", x: x("mode_ultracode", 0.95)}, want: "xhigh", wantMode: "ultracode", wantWork: "xhigh", wantWorkMode: "ultracode"},
		{name: "pas besoin d'ultracode turns it off", tier: "xhigh", mode: "ultracode", prompt: "pas besoin d'ultracode, corrige juste le message d'erreur", ask: "explicit_mode_off",
			jev: fa{tier: "low", conf: 0.9, ultra: 0.1, rel: "extend", x: x("mode_off", 0.9)}, want: "xhigh", wantWork: "xhigh"},
		{name: "a follow-up keeps the mode on", tier: "xhigh", mode: "ultracode", prompt: "et vérifie aussi le module de facturation",
			jev: fa{tier: "medium", conf: 0.9, ultra: 0.05, rel: "extend"}, want: "xhigh", wantMode: "ultracode", wantWork: "xhigh", wantWorkMode: "ultracode"},
		{name: "a mode kept on a tier drop raises the tier to what it runs", tier: "xhigh", mode: "ultracode", prompt: "now write the migration notes for ops",
			jev: fa{tier: "low", conf: 0.9, ultra: 0.4, rel: "new_task"}, want: "xhigh", wantMode: "ultracode", wantWork: "xhigh", wantWorkMode: "ultracode", newGoal: true},
		{name: "a prompt typed mid-turn never lowers", tier: "high", transcript: busy, prompt: "ah et les prix en euros TTC",
			jev: fa{tier: "low", conf: 0.98, rel: "new_task"}, want: "high", wantWork: "high"},
		{name: "mid-turn, above a lower work: not below the turn", tier: "xhigh", work: "medium", transcript: busy, prompt: "btw the staging DB is read-only",
			jev: fa{tier: "low", conf: 0.98, rel: "inform"}, want: "xhigh", wantWork: "xhigh"},
		{name: "a peer message never lowers", tier: "xhigh", prompt: `<cross-session-message from="docs">FYI the staging API moved to v2 [effort:low]</cross-session-message>`,
			jev: fa{tier: "low", conf: 0.95, rel: "new_task"}, want: "xhigh", wantWork: "xhigh"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sid := fmt.Sprint("wip", i)
			workSession(t, env, sid, c.tier, c.mode, cmp.Or(c.work, c.tier))
			fj.answers = []fa{c.jev}
			n := fj.calls()
			in := map[string]any{"session_id": sid, "prompt": c.prompt, "cwd": t.TempDir()}
			if c.transcript != "" {
				in["transcript_path"] = c.transcript
			}
			run(t, env, "decide", in)
			if fj.calls() != n+1 {
				t.Fatalf("%d Jev calls", fj.calls()-n)
			}
			q := fj.last().Questions
			if q[jev.QRelation].Type != "choice" {
				t.Errorf("no relation question: %v", q)
			}
			if c.ask != "" && q[c.ask].Type != "noul" {
				t.Errorf("no %s question: %v", c.ask, q)
			}
			s, _ := env.State.Load(sid)
			if s.Main.Tier != c.want || s.Main.Mode != c.wantMode || s.Pin != "" {
				t.Errorf("decision %s +%q (pin %q), want %s +%q", s.Main.Tier, s.Main.Mode, s.Pin, c.want, c.wantMode)
			}
			if eff := env.Catalog.Tier(catalog.ScopeMain, s.Main.Tier).Effort; s.Main.Effort != eff {
				t.Errorf("tier %s stored with effort %s", s.Main.Tier, s.Main.Effort)
			}
			if w := s.Work; w == nil || w.Tier != c.wantWork || w.Mode != c.wantWorkMode || (w.Goal != workGoal) != c.newGoal {
				t.Errorf("work in progress %+v, want %s +%q (new goal %v)", w, c.wantWork, c.wantWorkMode, c.newGoal)
			}
		})
	}
}

// A go-ahead carries the work in progress on: after a wrap-up lowered the
// tier, "vas-y" brings the work's tier and mode back without asking Jev; a
// go-ahead to a proposal is routed, not below the work.
func TestGoAheadRestoresWork(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	cwd := t.TempDir()
	decide := func(sid, p, tp string) {
		run(t, env, "decide", map[string]any{"session_id": sid, "prompt": p, "cwd": cwd, "transcript_path": tp})
	}
	// A side question answered at low (an older session, or one Jev read as
	// separate); the work in progress is still xhigh.
	workSession(t, env, "g1", "low", "", "xhigh")
	n := fj.calls()
	decide("g1", "Oui, vas-y !", "")
	s, _ := env.State.Load("g1")
	if fj.calls() != n || s.Main.Tier != "xhigh" || s.Main.Effort != "xhigh" || s.PendingEffort == nil || s.PendingEffort.Effort != "xhigh" {
		t.Fatalf("go-ahead after a side question: %d calls, %+v, pending %+v", fj.calls()-n, s.Main, s.PendingEffort)
	}
	all, _ := ledger.Decisions(env.Cfg.Ledger)
	if d := all[len(all)-1]; d.Hold != "go-ahead: back to the work in progress" || d.From != "low" || d.Chosen != "xhigh" || d.Kept {
		t.Errorf("ledger = %+v", d)
	}
	// The same flow, live: a wrap-up lowers, the go-ahead restores the mode too.
	workSession(t, env, "g2", "xhigh", "ultracode", "xhigh")
	fj.answers = []fa{{tier: "low", conf: 0.95, ultra: 0.1, rel: "wrap_up"}}
	decide("g2", "write the commit message", "")
	if s, _ = env.State.Load("g2"); s.Main.Tier != "low" || s.Main.Mode != "" || s.Work.Mode != "ultracode" {
		t.Fatalf("wrap-up: %+v, work %+v", s.Main, s.Work)
	}
	n = fj.calls()
	decide("g2", "ok, go", "")
	if s, _ = env.State.Load("g2"); fj.calls() != n || s.Main.Tier != "xhigh" || s.Main.Mode != "ultracode" || !s.UltracodeOn {
		t.Fatalf("go-ahead after a wrap-up: %+v", s.Main)
	}
	// Unchanged: only logged as kept.
	decide("g2", "continue", "")
	if s, _ = env.State.Load("g2"); fj.calls() != n || s.Main.Tier != "xhigh" {
		t.Fatalf("plain go-ahead: %+v", s.Main)
	}
	// A session from before the work in progress was recorded: its
	// decision in force is the work, kept once a wrap-up lowers the tier.
	warmSession(t, env, "g4", "xhigh", 300_000)
	fj.answers = []fa{{tier: "low", conf: 0.95, rel: "wrap_up"}}
	decide("g4", "résume ce que tu as fait", "")
	decide("g4", "vas-y", "")
	if s, _ = env.State.Load("g4"); s.Main.Tier != "xhigh" || s.Work == nil || s.Work.Tier != "xhigh" {
		t.Fatalf("older session: %+v, work %+v", s.Main, s.Work)
	}
	n = fj.calls()
	// A go-ahead to a proposal is routed, without the relation question,
	// and not below the work in progress.
	tp := filepath.Join(cwd, "t.jsonl")
	os.WriteFile(tp, []byte(`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"Both call sites read the lease without the lock. Want me to fix the second one too?"}]}}`+"\n"), 0o600)
	workSession(t, env, "g3", "xhigh", "", "xhigh")
	fj.answers = []fa{{tier: "low", conf: 0.9}}
	decide("g3", "yes", tp)
	if s, _ = env.State.Load("g3"); fj.calls() != n+1 || s.Main.Tier != "xhigh" || fj.last().Questions[jev.QRelation].Type != "" {
		t.Fatalf("go-ahead to a proposal: %+v, questions %v", s.Main, fj.last().Questions)
	}
	for _, p := range []string{"oui, vas-y", "ok, go", "yes, do it", "Parfait, fonce !", "let's go", "go for it", "ship it", "on y va", "c'est parti", "oui continue", "continue then"} {
		if !router.GoAhead(p) {
			t.Errorf("GoAhead(%q) = false", p)
		}
	}
	for _, p := range []string{"continue?", "go, and also add rate limiting", "oui mais garde l'ancienne API", "continue the refactor of the payment module"} {
		if router.GoAhead(p) {
			t.Errorf("GoAhead(%q) = true", p)
		}
	}
}

// A pin ([effort:x], /effort) keeps the ultracode mode unless its effort
// is below the mode's own, and never erases the work in progress.
func TestPinsKeepTheMode(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	decide := func(p string) *state.Session {
		run(t, env, "decide", map[string]any{"session_id": "p1", "prompt": p, "cwd": t.TempDir()})
		s, _ := env.State.Load("p1")
		return s
	}
	workSession(t, env, "p1", "xhigh", "ultracode", "xhigh")
	if s := decide("[effort:max] go, this one matters"); s.Main.Tier != "max" || s.Main.Mode != "ultracode" || !s.Main.Workflows || s.Pin != "max" {
		t.Fatalf("[effort:max] in an ultracode session: %+v", s.Main)
	}
	if s := decide("[effort:high] now the easy part"); s.Main.Tier != "high" || s.Main.Mode != "" || s.Work == nil || s.Work.Tier != "xhigh" || s.Work.Mode != "ultracode" {
		t.Fatalf("[effort:high]: %+v, work %+v", s.Main, s.Work)
	}
}

// Another model asked for in words is pinned like a [model:x] tag.
func TestModelAskedInWords(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	workSession(t, env, "m1", "high", "", "high")
	fj.answers = []fa{{tier: "medium", conf: 0.9, rel: "new_task", x: map[string]float64{"model_claude-sonnet-5-5": 0.92}}}
	run(t, env, "decide", map[string]any{"session_id": "m1", "prompt": "utilise sonnet pour résumer les logs d'hier", "cwd": t.TempDir()})
	s, _ := env.State.Load("m1")
	if s.PinModel != "claude-sonnet-5-5" || s.Main.Model != "claude-sonnet-5-5" || s.Main.Effort != "medium" || s.PinSource != "prompt" {
		t.Fatalf("model asked in words: pin %q, main %+v", s.PinModel, s.Main)
	}
	// Only mentioned: routing goes on.
	workSession(t, env, "m2", "high", "", "high")
	fj.answers = []fa{{tier: "high", conf: 0.9, rel: "side_question", x: map[string]float64{"model_claude-sonnet-5-5": 0.05}}}
	run(t, env, "decide", map[string]any{"session_id": "m2", "prompt": "is sonnet cheaper than opus for this?", "cwd": t.TempDir()})
	if s, _ := env.State.Load("m2"); s.PinModel != "" || s.Main.Model != "claude-opus-5-5" {
		t.Fatalf("a mention pinned the model: %+v", s.Main)
	}
}

// The work in progress survives a compaction, and the decision on the
// summary can't go below it.
func TestCompactionKeepsWork(t *testing.T) {
	fj := &fakeJev{}
	env := setup(t, fj)
	workSession(t, env, "c2", "xhigh", "ultracode", "xhigh")
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(tp, []byte(`{"type":"system","subtype":"compact_boundary"}`+"\n"+
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"We were auditing every handler for injection; next: the admin routes."}}`+"\n"), 0o600)
	fj.answers = []fa{{tier: "low", conf: 0.9, ultra: 0.1}}
	t.Setenv(CompactEnv, fmt.Sprint(time.Now().UnixNano()))
	run(t, env, "session-start", map[string]any{"session_id": "c2", "source": "compact", "transcript_path": tp})
	s, _ := env.State.Load("c2")
	if s.Main.Tier != "xhigh" || s.Main.Mode != "ultracode" || s.Work == nil || s.Work.Goal != workGoal {
		t.Fatalf("after compaction: %+v, work %+v", s.Main, s.Work)
	}
	if q := fj.last().Questions; q[jev.QRelation].Type != "" {
		t.Errorf("relation asked without a prompt: %v", q)
	}
	// The next prompt sees the work in progress (recent prompts are gone).
	t.Setenv(CompactEnv, "")
	fj.answers = []fa{{tier: "medium", conf: 0.9, ultra: 0.1, rel: "continue"}}
	run(t, env, "decide", map[string]any{"session_id": "c2", "prompt": "on reprend", "cwd": t.TempDir(), "transcript_path": tp})
	st := fj.last().State.(map[string]any)
	if wip, _ := st["work_in_progress"].(map[string]any); st["phase"] != "post_compact" || wip["goal"] != workGoal || wip["level"] != "xhigh" {
		t.Errorf("state = %v", st)
	}
	if s, _ = env.State.Load("c2"); s.Main.Tier != "xhigh" || s.Main.Mode != "ultracode" {
		t.Errorf("after the first prompt: %+v", s.Main)
	}
}
