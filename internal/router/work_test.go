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
