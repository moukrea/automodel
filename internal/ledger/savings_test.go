package ledger

import (
	"strings"
	"testing"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
)

func TestEstimateSavings(t *testing.T) {
	c, _, err := catalog.Load("../../catalog.toml", time.Now(), 3650)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Join([]string{
		`{"kind":"usage","ts":"2026-09-27T10:00:00Z","routed":true,"scope":"main","model":"claude-opus-5-5","effort":"low","input_tokens":10,"output_tokens":1000,"cache_read_input_tokens":100000}`,
		`{"kind":"usage","ts":"2026-09-27T10:01:00Z","routed":true,"scope":"main","model":"claude-opus-5-5","effort":"xhigh","output_tokens":1000}`,
		`{"kind":"usage","ts":"2026-09-27T10:02:00Z","routed":false,"scope":"main","model":"claude-opus-5-5","output_tokens":999999}`,
		`{"kind":"decision","ts":"2026-09-27T10:02:00Z"}`,
	}, "\n")
	s, err := EstimateSavings(strings.NewReader(lines), time.Time{}, c, "claude-opus-5-5", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests != 2 {
		t.Fatalf("requests = %d (unrouted must be ignored)", s.Requests)
	}
	low := costPerTask(c, "claude-opus-5-5", "low")
	xh := costPerTask(c, "claude-opus-5-5", "xhigh")
	out := 1000 * c.Model("claude-opus-5-5").Price.Output / 1e6
	in := (10*c.Model("claude-opus-5-5").Price.Input + 100000*c.Model("claude-opus-5-5").Price.CacheRead) / 1e6
	want := in + out*xh/low + out // only the low request's output scales
	if diff := s.BaselineUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("baseline = %v, want %v", s.BaselineUSD, want)
	}
	if s.SavedUSD <= 0 || s.SavedPct <= 0 {
		t.Fatalf("no savings: %+v", s)
	}
}
