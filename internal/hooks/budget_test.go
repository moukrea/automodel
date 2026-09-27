package hooks

import (
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/statusline"
)

// Over the daily cap, routing stays at or below max_tier_when_over, even on
// a go-ahead; a pin still wins; the status line and the ledger say so.
func TestBudgetCap(t *testing.T) {
	fj := &fakeJev{answers: []fa{{tier: "xhigh", conf: 0.95, ultra: 0.9, cont: 0.1}}}
	env := setup(t, fj)
	env.Cfg.Budget.USDPerDay = 5
	markJev(t, env, "s1")
	cwd := t.TempDir()
	prompt := func(p string) *state.Session {
		run(t, env, "decide", map[string]any{"session_id": "s1", "prompt": p, "cwd": cwd})
		s, _ := env.State.Load("s1")
		return s
	}
	if s := prompt("Audit the whole codebase for injection bugs"); s.Main.Tier != "xhigh" || s.Main.Mode != "ultracode" {
		t.Fatalf("under budget: %+v", s.Main)
	}
	if strings.Contains(statusline.Render(env, &state.Session{}, time.Now()), "budget") {
		t.Error("budget shown under the cap")
	}

	env.State.AddSpend(time.Now(), 6)
	s := prompt("ok go") // a go-ahead no longer keeps a tier above the cap
	if s.Main.Tier != "medium" || s.Main.Mode != "" {
		t.Fatalf("over budget: %+v", s.Main)
	}
	all, _ := ledger.Decisions(env.Cfg.Ledger)
	if d := all[len(all)-1]; d.BudgetCap != "xhigh" || d.Chosen != "medium" {
		t.Errorf("ledger = %+v", d)
	}
	if got := statusline.Render(env, s, time.Now()); !strings.Contains(got, "⚠ budget") {
		t.Errorf("status line = %q", got)
	}
	if s := prompt("[effort:max] this one matters"); s.Main.Tier != "max" {
		t.Errorf("pin capped: %+v", s.Main)
	}

	// The session cap works the same way; a new day resets the daily one.
	env.Cfg.Budget.USDPerDay, env.Cfg.Budget.USDPerSession = 0, 1
	env.State.Update("s2", func(s *state.Session) bool { s.TotalUSD = 2; return true })
	if env.BudgetCap("s2", "main") == nil || env.BudgetCap("s1", "main") != nil {
		t.Error("session cap")
	}
	env.Cfg.Budget.USDPerDay = 5
	env.Now = func() time.Time { return time.Now().Add(24 * time.Hour) }
	if env.BudgetCap("s1", "main") != nil {
		t.Error("daily cap not reset the next day")
	}
}
