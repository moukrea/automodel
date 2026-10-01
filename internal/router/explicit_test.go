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

// Every prompt gets every request a session can be asked for, whatever its
// words (Jev tells a request from a mention): the efforts of the session's
// model, more thinking, the workflow mode and its refusal, and the other
// main models that have a name.
func TestExplicitRequests(t *testing.T) {
	c := testCatalog(t)
	for _, tc := range []struct{ model, want string }{
		{"claude-opus-5-5", "effort_low effort_medium effort_high effort_xhigh effort_max effort_more mode_ultracode mode_off model_claude-fable-5-1 model_claude-sonnet-5-5"},
		{"claude-sonnet-5-5", "effort_low effort_medium effort_high effort_xhigh effort_max effort_more mode_ultracode mode_off model_claude-fable-5-1 model_claude-opus-5-5"},
		// Haiku can't run a main session: never a model request; its efforts map to the default tiers.
		{"claude-haiku-4-5", "effort_low effort_medium effort_high effort_xhigh effort_max effort_more mode_ultracode mode_off model_claude-fable-5-1 model_claude-opus-5-5 model_claude-sonnet-5-5"},
	} {
		var got []string
		for _, x := range ExplicitRequests(c, tc.model) {
			got = append(got, strings.TrimPrefix(x.ID(), "explicit_"))
			if x.Kind == "effort" && x.Value != "more" && c.Tier(catalog.ScopeMain, x.Tier) == nil {
				t.Errorf("%s: %s maps to no tier", tc.model, x.ID())
			}
		}
		if strings.Join(got, " ") != tc.want {
			t.Errorf("%s: %v, want %q", tc.model, got, tc.want)
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
	for _, x := range ExplicitRequests(c, opus) {
		th[strings.TrimPrefix(x.ID(), "explicit_")] = RequestThreshold(c, x, xhigh)
		if got := RequestThreshold(c, x, nil); got != ExplicitThreshold(c, x.Kind) {
			t.Errorf("%s without work: %.2f", x.ID(), got)
		}
	}
	if th["effort_low"] != max(c.Meta.ExplicitThreshold(), LowerEffortP) || th["effort_max"] != c.Meta.ExplicitThreshold() || th["model_claude-sonnet-5-5"] != c.Meta.ExplicitModelThreshold() {
		t.Errorf("thresholds = %v", th)
	}
}
