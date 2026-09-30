package router

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
)

// NewStore is the catalog store for a config: the default catalog, or the
// user's custom tuning layered over it.
func NewStore(cfg *config.Config) *catalog.Store {
	s := &catalog.Store{Path: cfg.Catalog, Custom: CustomTuning(cfg), Exact: cfg.CatalogExact,
		LastGood: cfg.LastGoodCatalog(), StaleDays: cfg.StaleDays}
	if cfg.NoLastGood {
		s.LastGood = ""
	}
	return s
}

// LoadCatalog returns the catalog a config routes with, and its issues.
func LoadCatalog(cfg *config.Config) (*catalog.Catalog, catalog.Issues, error) {
	c, err := NewStore(cfg).Get()
	if err != nil {
		return nil, nil, err
	}
	return c, c.Validate(time.Now(), cfg.StaleDays), nil
}

// CustomTuning reports whether a config routes with its custom file.
func CustomTuning(cfg *config.Config) bool {
	switch {
	case cfg.CatalogExact || cfg.Tuning == "custom":
		return true
	case cfg.Tuning == "default":
		return false
	}
	// Not set: a catalog file the user edited is their tuning; the copy an
	// older install seeded (unedited) is not.
	data, err := os.ReadFile(cfg.Catalog)
	if err != nil {
		return false
	}
	if catalog.Shipped == nil {
		return true
	}
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
	if sum(data) == sum(catalog.Shipped) {
		return false
	}
	mark, _ := os.ReadFile(SeededMark(cfg))
	return sum(data) != strings.TrimSpace(string(mark))
}

// SeededMark is where older installs recorded the hash of the catalog they
// seeded next to the config.
func SeededMark(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir, "catalog.shipped.sha256")
}
