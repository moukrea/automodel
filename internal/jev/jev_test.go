package jev

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
)

func testCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, is, err := catalog.Load("../../catalog.toml", time.Now(), 3650)
	if err != nil || len(is.Errors()) > 0 {
		t.Fatal(err, is.Errors())
	}
	return c
}

// The shipped catalog carries the built-in wording, so a custom tuning
// starts from what the code would ask anyway.
func TestCatalogWordingIsBuiltIn(t *testing.T) {
	c := testCatalog(t)
	if c.Questions.Level[catalog.ScopeMain] != DefaultLevel[catalog.ScopeMain] {
		t.Error("questions.level.main differs from the built-in wording")
	}
	if !reflect.DeepEqual(*c.Questions.Relation, DefaultRelation) {
		t.Errorf("questions.relation differs from the built-in wording:\n%+v\n%+v", *c.Questions.Relation, DefaultRelation)
	}
	if !reflect.DeepEqual(*c.Questions.Explicit, DefaultExplicit) {
		t.Errorf("questions.explicit differs from the built-in wording")
	}
}

func TestQuestions(t *testing.T) {
	c := testCatalog(t)
	qs, ids := Questions(c, catalog.ScopeMain, Ask{})
	if len(ids) != 5 || qs[QLevel].Type != "score" || qs[QRelation].Type != "" {
		t.Fatalf("no work in progress: %v %v", ids, qs)
	}
	xs := []Explicit{
		{Kind: ExplicitEffort, Value: "xhigh", Label: "xhigh"},
		{Kind: ExplicitEffort, Value: ExplicitMore},
		{Kind: ExplicitMode, Value: ExplicitOff, Label: "ultracode"},
		{Kind: ExplicitModel, Value: "claude-sonnet-5-5", Label: "Sonnet 5.5"},
	}
	qs, _ = Questions(c, catalog.ScopeMain, Ask{Relation: true, Explicit: xs})
	rel := qs[QRelation]
	b, _ := json.Marshal(rel.Criteria)
	var opts map[string]struct {
		What     string   `json:"what"`
		NotFor   string   `json:"not_for"`
		Examples []string `json:"examples"`
	}
	json.Unmarshal(b, &opts)
	if rel.Type != "choice" || len(opts) != len(catalog.Relations)-1 || opts["side_question"].What == "" || len(opts["extend"].Examples) == 0 {
		t.Fatalf("relation = %s", b)
	}
	// Going back to paused work is only an option once there is some.
	if _, ok := opts[catalog.RelationResume]; ok {
		t.Errorf("resume offered without paused work: %s", b)
	}
	qs2, _ := Questions(c, catalog.ScopeMain, Ask{Relation: true, Resume: true})
	if o := qs2[QRelation].Criteria.(map[string]*catalog.Option)[catalog.RelationResume]; o == nil || !strings.Contains(o.What, "`paused_work.goal`") {
		t.Errorf("resume option = %+v", o)
	}
	for _, path := range []string{"`task`", "`work_in_progress.goal`", "`recent_prompts`", "`last_assistant`"} {
		if !strings.Contains(rel.Instructions, path) {
			t.Errorf("relation question doesn't name %s", path)
		}
	}
	for id, want := range map[string]string{
		"explicit_effort_xhigh":            "ask the assistant itself to use the xhigh reasoning effort for its own work on this prompt?",
		"explicit_effort_more":             "to use more thinking than so far",
		"explicit_mode_off":                "not to use the ultracode mode (several agents working in parallel",
		"explicit_model_claude-sonnet-5-5": "ask the assistant to run on the Sonnet 5.5 model itself for its own work",
	} {
		q := qs[id]
		crit, _ := q.Criteria.(map[string]string)
		if q.Type != "noul" || !strings.Contains(q.Instructions, want) || crit["true"] == "" || crit["false"] == "" {
			t.Errorf("%s = %+v", id, q)
		}
	}
	// A custom tuning can reword one option; the others stay built in.
	c.Questions.Relation = &catalog.Relation{Options: map[string]*catalog.Option{"inform": {What: "Only a fact."}}}
	c.Questions.Explicit = &catalog.Explicit{Effort: "effort level {v}"}
	qs, _ = Questions(c, catalog.ScopeMain, Ask{Relation: true, Explicit: xs[:1]})
	opt := qs[QRelation].Criteria.(map[string]*catalog.Option)
	if opt["inform"].What != "Only a fact." || opt["extend"] != DefaultRelation.Options["extend"] || qs[QRelation].Instructions != DefaultRelation.Question {
		t.Errorf("partial relation tuning: %+v", opt)
	}
	if !strings.Contains(qs["explicit_effort_xhigh"].Instructions, "to use effort level xhigh for") {
		t.Errorf("partial explicit tuning: %s", qs["explicit_effort_xhigh"].Instructions)
	}
}
