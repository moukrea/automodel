package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shipped(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../catalog.toml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMergeAndOverrides(t *testing.T) {
	base := shipped(t)
	over := []byte("[meta]\nunderprovision_penalty = 2.0\n[tiers.main.medium]\ncriteria = \"my medium\"\n[state]\nrecent_prompts = 3\n")
	merged, err := Merge(base, over)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(merged)
	if err != nil {
		t.Fatal(err)
	}
	if c.Meta.UnderprovisionPenalty != 2 || c.Tier(ScopeMain, "medium").Criteria != "my medium" || c.State.RecentPromptsN() != 3 {
		t.Errorf("overrides not applied: %v %q %d", c.Meta.UnderprovisionPenalty, c.Tier(ScopeMain, "medium").Criteria, c.State.RecentPromptsN())
	}
	// Everything else follows the default.
	d, _ := Parse(base)
	if c.Tier(ScopeMain, "medium").Model != d.Tier(ScopeMain, "medium").Model || len(c.Measurements) != len(d.Measurements) || c.Meta.JevModel != d.Meta.JevModel {
		t.Errorf("defaults lost in the merge")
	}
	ov, err := Overrides(base, over)
	if err != nil || len(ov) != 3 || ov[0].Key != "meta.underprovision_penalty" || ov[0].Default != 1.5 {
		t.Errorf("overrides = %+v, %v", ov, err)
	}
	// A key equal to the default isn't a change.
	if ov, _ := Overrides(base, []byte("[meta]\nunderprovision_penalty = 1.5\n")); len(ov) != 0 {
		t.Errorf("unchanged key listed: %+v", ov)
	}
}

func TestStoreTuning(t *testing.T) {
	base := shipped(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.toml")
	os.WriteFile(path, []byte("[meta]\nunderprovision_penalty = 2.5\n"), 0o600)

	def := &Store{Path: path, Default: base, StaleDays: 3650}
	if c, err := def.Get(); err != nil || c.Meta.UnderprovisionPenalty != 1.5 || def.Source() != "default" {
		t.Fatalf("default tuning: %v %v %s", c.Meta.UnderprovisionPenalty, err, def.Source())
	}
	cus := &Store{Path: path, Custom: true, Default: base, StaleDays: 3650}
	if c, err := cus.Get(); err != nil || c.Meta.UnderprovisionPenalty != 2.5 || !strings.HasPrefix(cus.Source(), "custom: ") {
		t.Fatalf("custom tuning: %v %v", err, cus.Source())
	}
	// A broken custom file falls back to the default, never to nothing.
	os.WriteFile(path, []byte("[tiers.main.medium]\nmodel = \"ghost\"\n"), 0o600)
	bad := &Store{Path: path, Custom: true, Default: base, StaleDays: 3650}
	if c, err := bad.Get(); err != nil || c.Tier(ScopeMain, "medium").Model == "ghost" {
		t.Fatalf("broken custom file: %v", err)
	}
}
