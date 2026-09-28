package router

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
)

func TestCustomTuning(t *testing.T) {
	dir := t.TempDir()
	old := catalog.Shipped
	catalog.Shipped = []byte("[meta]\nschema = 1 # new release\n")
	t.Cleanup(func() { catalog.Shipped = old })
	cfg := config.Default()
	cfg.StateDir, cfg.Catalog = dir, filepath.Join(dir, "catalog.toml")

	if CustomTuning(cfg) {
		t.Error("no file: custom")
	}
	seeded := []byte("[meta]\nschema = 1 # older release\n")
	os.WriteFile(cfg.Catalog, seeded, 0o600)
	h := sha256.Sum256(seeded)
	os.WriteFile(SeededMark(cfg), []byte(hex.EncodeToString(h[:])+"\n"), 0o600)
	if CustomTuning(cfg) {
		t.Error("unedited seeded copy: custom")
	}
	os.WriteFile(cfg.Catalog, []byte("[meta]\nschema = 1 # edited\n"), 0o600)
	if !CustomTuning(cfg) {
		t.Error("edited file: not custom")
	}
	cfg.Tuning = "default"
	if CustomTuning(cfg) {
		t.Error("tuning = default: custom")
	}
	cfg.Tuning = "custom"
	os.Remove(cfg.Catalog)
	if !CustomTuning(cfg) {
		t.Error("tuning = custom: not custom")
	}
}
