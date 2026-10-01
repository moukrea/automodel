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
	for _, q := range []struct {
		name      string
		got, want *catalog.Relation
	}{{"effort", c.Questions.ExplicitEffort, &DefaultExplicitEffort}, {"mode", c.Questions.ExplicitMode, &DefaultExplicitMode}, {"model", c.Questions.ExplicitModel, &DefaultExplicitModel}} {
		if q.got == nil || !reflect.DeepEqual(*q.got, *q.want) {
			t.Errorf("questions.explicit_%s differs from the built-in wording", q.name)
		}
	}
	if c.Questions.Offer == nil || *c.Questions.Offer != DefaultOffer {
		t.Errorf("questions.offer differs from the built-in wording")
	}
}

func TestQuestions(t *testing.T) {
	c := testCatalog(t)
	qs, ids := Questions(c, catalog.ScopeMain, Ask{})
	if len(ids) != 5 || qs[QLevel].Type != "score" || qs[QRelation].Type != "" || qs[QOffer].Type != "" {
		t.Fatalf("no work in progress: %v %v", ids, qs)
	}
	// The offer yes/no only after a detour whose paused work needs more.
	if o := func() map[string]Question {
		q, _ := Questions(c, catalog.ScopeMain, Ask{Relation: true, Resume: true, Offer: true})
		return q
	}()[QOffer]; o.Type != "noul" || !strings.Contains(o.Instructions, "`paused_work.goal`") {
		t.Errorf("offer question = %+v", o)
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
	// A question about automodel's routing is an aside, not a side question
	// (held-out run 1 held "why did automodel keep this session at xhigh?"
	// at the work's xhigh); a go-ahead on a done work may only acknowledge it.
	if !strings.Contains(opts["aside"].What, "automodel's routing") || strings.Contains(opts["side_question"].What, "effort") ||
		!strings.Contains(opts["side_question"].NotFor, "automodel's routing") || !strings.Contains(opts["continue"].NotFor, "`work_in_progress.done`") {
		t.Errorf("routing questions and acknowledgements: %s", b)
	}
	// Going back to paused work is only an option once there is some.
	if _, ok := opts[catalog.RelationResume]; ok {
		t.Errorf("resume offered without paused work: %s", b)
	}
	qs2, _ := Questions(c, catalog.ScopeMain, Ask{Relation: true, Resume: true})
	if o := qs2[QRelation].Criteria.(map[string]*catalog.Option)[catalog.RelationResume]; o == nil || !strings.Contains(o.What, "`paused_work.goal`") {
		t.Errorf("resume option = %+v", o)
	}
	// "yes" to the offer to get back to the paused work is a resume, not a
	// continue of the detour; "yes" to a wrap-up step of the detour is a
	// wrap-up (round 4: both went through without Jev and resumed).
	o2 := qs2[QRelation].Criteria.(map[string]*catalog.Option)
	if !strings.Contains(o2["resume"].What, "offer to get back") || !strings.Contains(o2["continue"].NotFor, "`paused_work.goal`") || !strings.Contains(o2["wrap_up"].What, "paused") {
		t.Errorf("detour proposals: resume %+v, continue %+v, wrap_up %+v", o2["resume"], o2["continue"], o2["wrap_up"])
	}
	for _, path := range []string{"`task`", "`work_in_progress.goal`", "`recent_prompts`", "`last_assistant`"} {
		if !strings.Contains(rel.Instructions, path) {
			t.Errorf("relation question doesn't name %s", path)
		}
	}
	// One Choice per kind of request, its options competing, plus none.
	for id, want := range map[string][]string{
		"explicit_effort": {"none", "xhigh", "more"},
		"explicit_mode":   {"none", "off"},
		"explicit_model":  {"none", "claude-sonnet-5-5"},
	} {
		q := qs[id]
		opts, _ := q.Criteria.(map[string]*catalog.Option)
		if q.Type != "choice" || len(opts) != len(want) {
			t.Errorf("%s = %+v", id, q)
		}
		for _, o := range want {
			if opts[o] == nil || opts[o].What == "" || strings.Contains(opts[o].What+opts[o].NotFor+strings.Join(opts[o].Examples, ""), "{v}") {
				t.Errorf("%s option %s = %+v", id, o, opts[o])
			}
		}
	}
	if m := qs["explicit_model"].Criteria.(map[string]*catalog.Option)["claude-sonnet-5-5"]; !strings.Contains(m.What, "switch to Sonnet 5.5") {
		t.Errorf("model option = %+v", m)
	}
	if q := qs["explicit_mode"]; !strings.Contains(q.Instructions, "in the ultracode mode") {
		t.Errorf("mode question = %s", q.Instructions)
	}
	// A custom tuning can reword one option; the others stay built in.
	c.Questions.Relation = &catalog.Relation{Options: map[string]*catalog.Option{"inform": {What: "Only a fact."}}}
	c.Questions.ExplicitEffort = &catalog.Relation{Options: map[string]*catalog.Option{"xhigh": {What: "Extra high, by name."}}}
	qs, _ = Questions(c, catalog.ScopeMain, Ask{Relation: true, Explicit: xs[:1]})
	opt := qs[QRelation].Criteria.(map[string]*catalog.Option)
	if opt["inform"].What != "Only a fact." || opt["extend"] != DefaultRelation.Options["extend"] || qs[QRelation].Instructions != DefaultRelation.Question {
		t.Errorf("partial relation tuning: %+v", opt)
	}
	eo := qs["explicit_effort"].Criteria.(map[string]*catalog.Option)
	if eo["xhigh"].What != "Extra high, by name." || eo["none"].What != DefaultExplicitEffort.Options["none"].What || qs["explicit_effort"].Instructions != DefaultExplicitEffort.Question {
		t.Errorf("partial explicit tuning: %+v", eo)
	}
}

// Choice answers read as request IDs (none left out); saved yes/no answers
// still read.
func TestExplicitProbs(t *testing.T) {
	yes := 0.9
	got := ExplicitProbs(map[string]Answer{
		"explicit_effort":                  {Type: "choice", Probabilities: map[string]float64{"none": 0.1, "low": 0.85, "medium": 0.05}},
		"explicit_mode":                    {Type: "choice", Probabilities: map[string]float64{"none": 0.9, "off": 0.1}},
		"explicit_model_claude-sonnet-5-5": {Type: "noul", Noul: &yes},
		"relation":                         {Type: "choice", Probabilities: map[string]float64{"continue": 1}},
	})
	want := map[string]float64{"explicit_effort_low": 0.85, "explicit_effort_medium": 0.05, "explicit_mode_off": 0.1, "explicit_model_claude-sonnet-5-5": 0.9}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ExplicitProbs = %v", got)
	}
}
