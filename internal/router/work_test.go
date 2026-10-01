package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/policy"
	"github.com/moukrea/automodel/internal/state"
)

func testEnv(t *testing.T) *Env {
	t.Helper()
	d := t.TempDir()
	cfg := config.Default()
	cfg.StateDir, cfg.Ledger = d, filepath.Join(d, "ledger.jsonl")
	return &Env{Cfg: cfg, Catalog: testCatalog(t), State: state.Store{Dir: d}, Ledger: ledger.Ledger{Path: cfg.Ledger}, Now: time.Now}
}

// The hold names the relation that holds: the likeliest that follows the
// work up, not a separate one likelier on its own but below the threshold
// with the others (live: "follow-up of the work in progress (new_task
// 0.45)").
func TestHoldNamesTheRelationThatHolds(t *testing.T) {
	e := testEnv(t)
	req := Request{Scope: catalog.ScopeMain, Work: &state.Work{Tier: "xhigh", Goal: "audit the handlers for missing auth checks"}}
	rd := Reading{probs: map[string]float64{"low": 0.9, "medium": 0.1}, conf: 0.9, top: "low",
		relation: map[string]float64{catalog.RelationNewTask: 0.45, catalog.RelationExtend: 0.40, catalog.RelationContinue: 0.15}}
	v := e.Judge(req, rd, nil, policy.RepoPolicy{}, policy.Params{Penalty: 1.5, Scale: 1})
	if v.Tier.ID != "xhigh" || v.Hold != "follow-up of the work in progress (extend 0.40)" {
		t.Errorf("decision %s, hold %q", v.Tier.ID, v.Hold)
	}
	rd.relation = map[string]float64{catalog.RelationSideQuestion: 0.9, catalog.RelationAside: 0.1}
	if v = e.Judge(req, rd, nil, policy.RepoPolicy{}, policy.Params{Penalty: 1.5, Scale: 1}); !strings.HasPrefix(v.Hold, "follow-up of the work in progress (side_question 0.90)") {
		t.Errorf("hold %q", v.Hold)
	}
}

// A wrap-up that closes a kept detour runs at the higher of the detour's
// level and Jev's at most (live: "commit it" closing a low detour read low
// 0.53 / xhigh 0.39 and ran at high, the policy's pick); what the prompt
// asks for (ultrathink) still counts. A wrap-up of the work itself, with
// no kept detour, keeps the pick.
func TestDetourDoneRunsAtTheDetourLevel(t *testing.T) {
	e := testEnv(t)
	work := &state.Work{Tier: "xhigh", Goal: "fix the oversell race in checkout"}
	kept := &state.Work{Tier: "low", Goal: "fix the port in the README", Kept: true}
	rd := Reading{probs: map[string]float64{"low": 0.53, "high": 0.08, "xhigh": 0.39}, conf: 0.4, top: "low",
		relation: map[string]float64{catalog.RelationWrapUp: 0.83, catalog.RelationContinue: 0.17}}
	p := policy.Params{Penalty: 1.5, Scale: 1}
	free := Request{Scope: catalog.ScopeMain, Work: work}
	pick := e.Judge(free, rd, nil, policy.RepoPolicy{}, p)
	if pick.Tier.ID == "low" || pick.Work == nil || pick.Work.Kind != WorkDone {
		t.Fatalf("wrap-up of the work: %s, work %+v (the pick should be above low)", pick.Tier.ID, pick.Work)
	}
	req := Request{Scope: catalog.ScopeMain, Work: work, Paused: kept}
	if v := e.Judge(req, rd, nil, policy.RepoPolicy{}, p); v.Tier.ID != "low" || v.Mode != "" || v.Work == nil || v.Work.Kind != WorkDetourDone {
		t.Errorf("wrap-up closing the kept detour: %s/%s, work %+v", v.Tier.ID, v.Mode, v.Work)
	}
	req.MinTier = "xhigh"
	if v := e.Judge(req, rd, nil, policy.RepoPolicy{}, p); v.Tier.ID != "xhigh" {
		t.Errorf("ultrathink on the wrap-up closing the kept detour: %s", v.Tier.ID)
	}
}
