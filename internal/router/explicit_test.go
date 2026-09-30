package router

import (
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

// The regex only finds the words that may make a request (Jev tells a
// request from a mention): each case lists the questions a prompt gets.
func TestExplicitCandidates(t *testing.T) {
	c := testCatalog(t)
	opus := "claude-opus-5-5"
	for _, tc := range []struct{ prompt, model, want string }{
		{"fais la suite en xhigh", opus, "effort_xhigh"},
		{"passe en low pour la suite, c'est mécanique", opus, "effort_low"},
		{"switch to high effort for the migration", opus, "effort_high"},
		{"use medium here", opus, "effort_medium"},
		{"do this at X-high please", opus, "effort_xhigh"},
		{"max retries is 3", opus, "effort_max"}, // a mention: Jev says no
		{"the high part of the range overflows", opus, ""},
		{"medium-sized files only", opus, ""},
		{"think harder about the retry path", opus, "effort_more"},
		{"réfléchis à fond avant de toucher au verrou", opus, "effort_more"},
		{"mets le paquet sur ce bug", opus, "effort_more"},
		{"revois-moi tout ça en profondeur", opus, "effort_more"},
		// Around the word effort, niveau or reasoning, in both languages.
		{"set the effort to medium", opus, "effort_medium"},
		{"raise the effort to high", opus, "effort_high"},
		{"change the effort level to low", opus, "effort_low"},
		{"high reasoning effort please", opus, "effort_high"},
		{"reasoning effort: high", opus, "effort_high"},
		{"mets l'effort à low pour la suite", opus, "effort_low"},
		{"passe l'effort à low", opus, "effort_low"},
		{"effort élevé stp", opus, "effort_high"},
		{"Effort faible, c'est mécanique", opus, "effort_low"},
		{"niveau moyen pour ça", opus, "effort_medium"},
		{"effort max sur ce bug", opus, "effort_max"},
		// Lowerings and caps.
		{"drop to medium for what's left, it's boilerplate", opus, "effort_medium"},
		{"go down to low for the rest", opus, "effort_low"},
		{"bring it down to medium, the rest is renames", opus, "effort_medium"},
		{"medium is enough for this", opus, "effort_medium"},
		{"low should be enough for the changelog", opus, "effort_low"},
		{"low suffit pour ça", opus, "effort_low"},
		{"faible suffit pour ça", opus, "effort_low"},
		{"medium, ça suffit", opus, "effort_medium"},
		{"high c'est assez pour ce refacto", opus, "effort_high"},
		{"moyen c’est assez", opus, "effort_medium"},
		{"on peut redescendre à medium", opus, "effort_medium"},
		{"descends en low pour les tests qui restent", opus, "effort_low"},
		{"pour la suite en low", opus, "effort_low"},
		{"pour le reste, en faible", opus, "effort_low"},
		{"p99 went down to low double digits, expected?", opus, ""},
		{"go down to low effort for the tests", opus, "effort_low"},
		{"drop down to medium please", opus, "effort_medium"},
		{"drop to low right away, the rest is renames", opus, "effort_low"},
		{"drop to medium: it's boilerplate", opus, "effort_medium"},
		{"drop to low (the rest is renames)", opus, "effort_low"},
		{"go down to medium and finish the renames", opus, "effort_medium"},
		{"on peut redescendre à medium maintenant", opus, "effort_medium"},
		{"redescends a medium stp", opus, "effort_medium"},
		{"descendez en low pour le reste", opus, "effort_low"},
		{"tu peux baisser à medium pour la suite", opus, "effort_medium"},
		{"lower it to medium for the remaining fixtures", opus, "effort_medium"},
		{"le trafic baisse à faible régime la nuit", opus, ""},
		{"lower to high-level summaries only", opus, ""},
		{"tu peux redescendre à low pour macOS et Linux", opus, "effort_low"},
		{"Go down to high effort for the rest of this", opus, "effort_high"},
		{"low suffit pour la suite", opus, "effort_low"},
		{"Bon, moyen c'est assez pour ça", opus, "effort_medium"},
		// Idioms around a level word: no request, not even put to Jev.
		{"it comes down to high availability in the end", opus, ""},
		{"it all boils down to low latency on the hot path", opus, ""},
		{"narrow it down to low-level functions first", opus, ""},
		{"drop a low-priority job from the queue", opus, ""},
		{"drop a high-cardinality label from the metric", opus, ""},
		{"scroll down to the high score table", opus, ""},
		{"break it down to high level steps", opus, ""},
		{"la charge descend à faible charge la nuit", opus, ""},
		{"un seuil bas suffit pour l'alerte", opus, ""},
		{"le bouton descend en bas de page", opus, ""},
		{"mets le bouton en haut à droite", opus, ""},
		{"niveau bas pour ça", opus, "effort_low"},
		{"the queue drops to zero at night", opus, ""},
		{"is the low bit enough to tell them apart?", opus, ""},
		{"prices went down to lower levels", opus, ""},
		{"medium-sized batches are enough", opus, ""},
		// A mode word asks both questions: the mode and its refusal.
		{"fais ça en ultracode", opus, "mode_ultracode mode_off"},
		{"lance plusieurs agents en parallèle sur l'audit", opus, "mode_ultracode mode_off"},
		{"pas besoin d'ultracode ici", opus, "mode_ultracode mode_off"},
		{"fais-le sans workflow", opus, "mode_ultracode mode_off"},
		{"plus besoin d'ultracode, corrige juste ça", opus, "mode_ultracode mode_off"},
		{"no need for ultracode here", opus, "mode_ultracode mode_off"},
		{"n'utilise pas ultracode pour ça", opus, "mode_ultracode mode_off"},
		{"skip ultracode for this", opus, "mode_ultracode mode_off"},
		{"ultracode isn't needed", opus, "mode_ultracode mode_off"},
		{"pas la peine d'utiliser ultracode", opus, "mode_ultracode mode_off"},
		{"review the handlers in parallel please", opus, "mode_ultracode mode_off"},
		{"use subagents in parallel for the sweep", opus, "mode_ultracode mode_off"},
		{"spin up several agents on the audit", opus, "mode_ultracode mode_off"},
		{"fan out several agents in parallel over the packages", opus, "mode_ultracode mode_off"},
		{"split this across subagents, one per service", opus, "mode_ultracode mode_off"},
		{"répartis le travail sur plusieurs sous-agents", opus, "mode_ultracode mode_off"},
		{"fan the events out to every subscriber", opus, ""},
		{"utilise sonnet pour résumer ça", opus, "model_claude-sonnet-5-5"},
		{"avec Opus, en xhigh et en ultracode", opus, "effort_xhigh mode_ultracode mode_off"},
		{"a quick one for haiku", opus, ""}, // Haiku can't run a main session
		{"en high", "claude-haiku-4-5", "effort_high"},
		{"passe en xhigh et reviens sur opus", "claude-sonnet-5-5", "effort_xhigh model_claude-opus-5-5"}, // on a work's own model
		{"ultrathink: other places we read the lease without the lock?", opus, ""},
		{"rename the config loader", opus, ""},
	} {
		var got []string
		for _, x := range ExplicitCandidates(c, tc.prompt, tc.model) {
			got = append(got, strings.TrimPrefix(x.ID(), "explicit_"))
			if x.Kind == "effort" && x.Value != "more" && c.Tier(catalog.ScopeMain, x.Tier) == nil {
				t.Errorf("%q: %s maps to no tier", tc.prompt, x.ID())
			}
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%q: %v, want %q", tc.prompt, got, tc.want)
		}
	}
	if !Ultrathink("Ultrathink: is the lease renewed under the lock?") || Ultrathink("ultrathinking is a word?") {
		t.Error("Ultrathink")
	}
}

// An effort below the work in progress needs LowerEffortP at least; above
// it, or with no work, meta.explicit_threshold; a model its own threshold.
func TestRequestThreshold(t *testing.T) {
	c := testCatalog(t)
	xhigh := c.Tier(catalog.ScopeMain, "xhigh")
	opus := "claude-opus-5-5"
	th := map[string]float64{}
	for _, x := range ExplicitCandidates(c, "passe en low, puis en max, et sur sonnet", opus) {
		th[strings.TrimPrefix(x.ID(), "explicit_")] = RequestThreshold(c, x, xhigh)
		if got := RequestThreshold(c, x, nil); got != ExplicitThreshold(c, x.Kind) {
			t.Errorf("%s without work: %.2f", x.ID(), got)
		}
	}
	if th["effort_low"] != max(c.Meta.ExplicitThreshold(), LowerEffortP) || th["effort_max"] != c.Meta.ExplicitThreshold() || th["model_claude-sonnet-5-5"] != c.Meta.ExplicitModelThreshold() {
		t.Errorf("thresholds = %v", th)
	}
}
