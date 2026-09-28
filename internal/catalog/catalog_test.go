package catalog

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

var today = time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

func load(t *testing.T, path string) (*Catalog, Issues) {
	t.Helper()
	c, is, err := Load(path, today, 60)
	if err != nil {
		t.Fatal(err)
	}
	return c, is
}

func TestRepoCatalogIsValid(t *testing.T) {
	c, is := load(t, "../../catalog.toml")
	if errs := is.Errors(); len(errs) > 0 {
		t.Fatalf("errors: %v", errs)
	}
	if got := c.DefaultTier(ScopeMain).ID; got != "high" {
		t.Errorf("default main tier = %s", got)
	}
	var ids []string
	for _, tr := range c.TiersByRank(ScopeMain) {
		ids = append(ids, tr.ID)
	}
	if got := strings.Join(ids, ","); got != "haiku,low,medium,high,xhigh,max" {
		t.Errorf("main tiers by rank = %s", got)
	}
	dom := c.Dominance("v4.3.2")
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if by := dom.Dominators["claude-opus-5-5@"+e]; len(by) > 0 {
			t.Errorf("opus %s should be on the frontier, dominated by %v", e, by)
		}
	}
	if by := dom.Dominators["claude-sonnet-5@medium"]; len(by) == 0 {
		t.Error("sonnet medium should be dominated")
	}
	if by := dom.Dominators["claude-sonnet-5@low"]; len(by) > 0 {
		t.Error("sonnet low is only quasi-dominated")
	}
}

func TestInvalidCatalog(t *testing.T) {
	_, is := load(t, "../../testdata/catalogs/invalid.toml")
	want := []string{
		"meta.jev_model is required",
		`meta.default_main_tier "nope" is not a main tier`,
		"model a: active model needs api_id",
		`model b: status "bogus"`,
		"tier main.one: effort \"max\" not supported",
		"tier main.two: unknown model",
		"rank 1 already used",
		"tier main.three: criteria is empty",
		"tier main.three: model c is dominated, not active",
		"tier main.four: model d is restricted to scopes",
		"tier subagent.sub: model a needs an alias",
		"tier main.one: model a has a 1000-token window; main tiers need at least 1000000",
		"model d: context 400000 above 200000 needs long_context",
	}
	errs := is.Errors()
	for _, w := range want {
		found := false
		for _, e := range errs {
			if strings.Contains(e.Message, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing error %q in %v", w, errs)
		}
	}
}

func TestWarnings(t *testing.T) {
	_, is := load(t, "../../testdata/catalogs/warnings.toml")
	if errs := is.Errors(); len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	want := []string{
		`model c: status "retired" without a reason`,
		"tier main.cheap: no effort on a",
		"tier main.worse: config a@high is dominated by [a@low]",
		"model b: active without a measurement in benchmark_version v2",
		"measurement a@low: measured_at 2026-01-01 is older than 60 days",
		"meta.last_refresh 2026-01-01 is older than 60 days",
	}
	for _, w := range want {
		found := false
		for _, i := range is.Warnings() {
			if strings.Contains(i.Message, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing warning %q in %v", w, is.Warnings())
		}
	}
}

func TestDominates(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	a := Measurement{Index: 50, CostPerTask: 1, TimePerTaskS: f(100)}
	b := Measurement{Index: 50, CostPerTask: 1, TimePerTaskS: f(200)}
	if !Dominates(a, b) || Dominates(b, a) {
		t.Error("time should break the tie")
	}
	c := Measurement{Index: 50, CostPerTask: 1}
	if Dominates(a, c) || Dominates(c, a) {
		t.Error("equal on shared dimensions: no dominance")
	}
	d := Measurement{Index: 60, CostPerTask: 0.5, TimePerTaskS: f(300)}
	if Dominates(d, a) {
		t.Error("slower on a shared dimension: no dominance")
	}
}

// The skill's frontier.py must apply the same rules as the Go validator.
func TestFrontierPyAgrees(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err == nil {
		// Windows may only have the Microsoft Store's python3 stub.
		err = exec.Command(py, "--version").Run()
	}
	if err != nil {
		t.Skip("python3 not found")
	}
	script := "../../.claude/skills/refresh-model-catalog/scripts/frontier.py"
	files, _ := filepath.Glob("../../testdata/catalogs/*.toml")
	files = append(files, "../../catalog.toml")
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			cmd := exec.Command(py, script, f, "--json", "--today", "2026-09-26")
			cmd.Env = append(os.Environ(), "PYTHONUTF8=1") // UTF-8 stdout on Windows too
			out, _ := cmd.Output()
			var r struct {
				Errors, Warnings []Issue
			}
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatalf("frontier.py: %v\n%s", err, out)
			}
			_, is := load(t, f)
			goSet, pySet := norm(is), norm(append(r.Errors, r.Warnings...))
			if strings.Join(goSet, "\n") != strings.Join(pySet, "\n") {
				t.Errorf("go:\n  %s\npython:\n  %s", strings.Join(goSet, "\n  "), strings.Join(pySet, "\n  "))
			}
		})
	}
}

var noise = regexp.MustCompile(`[\[\]'",()]`)

func norm(is []Issue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Level+": "+strings.Join(strings.Fields(noise.ReplaceAllString(i.Message, "")), " "))
	}
	sort.Strings(out)
	return out
}

func TestStoreKeepsLastGood(t *testing.T) {
	dir := t.TempDir()
	good, _ := os.ReadFile("../../catalog.toml")
	path := filepath.Join(dir, "catalog.toml")
	os.WriteFile(path, good, 0o644)
	s := &Store{Path: path, LastGood: filepath.Join(dir, "state", "lastgood.toml")}
	c1, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	// Break the file: the proxy keeps the previous catalog.
	os.WriteFile(path, []byte("[meta]\nschema = 1\n"), 0o644)
	s.checked = time.Time{}
	c2, err := s.Get()
	if err != nil || c2 != c1 {
		t.Fatalf("expected previous catalog, got %v %v", c2, err)
	}
	// A fresh process (hook) falls back to the last good copy.
	s2 := &Store{Path: path, LastGood: s.LastGood}
	c3, err := s2.Get()
	if err != nil || c3.Meta.DefaultMainTier != "high" {
		t.Fatalf("expected last good catalog, got %v", err)
	}
}
