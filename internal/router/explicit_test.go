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
		{"fais ça en ultracode", opus, "mode_ultracode"},
		{"lance plusieurs agents en parallèle sur l'audit", opus, "mode_ultracode"},
		{"pas besoin d'ultracode ici", opus, "mode_ultracode mode_off"},
		{"fais-le sans workflow", opus, "mode_ultracode mode_off"},
		{"utilise sonnet pour résumer ça", opus, "model_claude-sonnet-5-5"},
		{"avec Opus, en xhigh et en ultracode", opus, "effort_xhigh mode_ultracode"},
		{"a quick one for haiku", opus, ""}, // Haiku can't run a main session
		{"en high", "claude-haiku-4-5", "effort_high"},
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
