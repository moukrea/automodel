package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	automodel "github.com/moukrea/automodel"
	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/router"
)

// tuningCmd: which catalog automodel routes with, and how to change it.
//
//	automodel tuning                 status (and what the custom file changes)
//	automodel tuning use default|custom
//	automodel tuning init [--full]   create the custom file
//	automodel tuning diff            the custom file's changes, with the defaults
//	automodel tuning show [--default]
//	automodel tuning path
func tuningCmd(cfg *config.Config, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "", "status":
		return tuningStatus(cfg)
	case "use":
		if len(args) != 1 || (args[0] != "default" && args[0] != "custom") {
			return errors.New("usage: automodel tuning use default|custom")
		}
		return tuningUse(cfg, args[0])
	case "init":
		fs := flag.NewFlagSet("tuning init", flag.ExitOnError)
		full := fs.Bool("full", false, "start from a copy of the whole default catalog instead of a template of the usual changes")
		force := fs.Bool("force", false, "overwrite an existing custom file")
		fs.Parse(args)
		return tuningInit(cfg, *full, *force)
	case "diff":
		return tuningDiff(cfg, os.Stdout)
	case "show":
		fs := flag.NewFlagSet("tuning show", flag.ExitOnError)
		def := fs.Bool("default", false, "print automodel's default catalog instead of the one routing uses")
		fs.Parse(args)
		if *def {
			_, err := os.Stdout.Write(automodel.Catalog)
			return err
		}
		return tuningShow(cfg)
	case "path":
		fmt.Println(cfg.Catalog)
		return nil
	}
	return fmt.Errorf("unknown tuning command %q (want use, init, diff, show or path)", sub)
}

func tuningStatus(cfg *config.Config) error {
	custom := router.CustomTuning(cfg)
	_, fileErr := os.Stat(cfg.Catalog)
	if !custom {
		fmt.Printf("tuning: default: automodel's own catalog, shipped with %s and updated with every release\n", version)
		switch {
		case fileErr == nil:
			n := countChanges(cfg)
			fmt.Printf("custom file: %s (not in use; %s)\n", cfg.Catalog, n)
			fmt.Println("use it: automodel tuning use custom")
		default:
			fmt.Println("customize: automodel tuning init, edit the file, then automodel tuning use custom")
		}
		return nil
	}
	how := "layered over automodel's default (" + version + ")"
	if data, err := os.ReadFile(cfg.Catalog); err == nil && catalog.IsWhole(data) {
		how = "a whole catalog, used as it is"
	}
	fmt.Printf("tuning: custom: %s, %s\n", cfg.Catalog, how)
	if cfg.Tuning == "" {
		fmt.Println("  (tuning isn't set in the config: custom because this file was edited)")
	}
	if fileErr != nil {
		fmt.Printf("  ⚠ %s is missing: routing uses the default\n", cfg.Catalog)
	} else if _, issues, err := router.LoadCatalog(cfg); err != nil || len(issues.Errors()) > 0 {
		fmt.Printf("  ⚠ invalid (%v%v): routing uses the default\n", err, issues.Errors())
	}
	if err := tuningDiff(cfg, os.Stdout); err != nil {
		return err
	}
	fmt.Println("back to the default: automodel tuning use default")
	return nil
}

func countChanges(cfg *config.Config) string {
	data, err := os.ReadFile(cfg.Catalog)
	if err != nil {
		return "unreadable"
	}
	ov, err := catalog.Overrides(automodel.Catalog, data)
	if err != nil {
		return "invalid TOML"
	}
	return fmt.Sprintf("%d change(s) from the default", len(ov))
}

func tuningDiff(cfg *config.Config, w *os.File) error {
	data, err := os.ReadFile(cfg.Catalog)
	if err != nil {
		fmt.Fprintf(w, "no custom file at %s (automodel tuning init creates one)\n", cfg.Catalog)
		return nil
	}
	ov, err := catalog.Overrides(automodel.Catalog, data)
	if err != nil {
		return fmt.Errorf("%s: %w", cfg.Catalog, err)
	}
	if len(ov) == 0 {
		fmt.Fprintln(w, "no change from the default")
		return nil
	}
	fmt.Fprintf(w, "%d change(s) from the default:\n", len(ov))
	for _, o := range ov {
		def := "new"
		if !o.New {
			def = "default " + catalog.Short(o.Default, 60)
		}
		fmt.Fprintf(w, "  %-44s %s  (%s)\n", o.Key, catalog.Short(o.Value, 60), def)
	}
	return nil
}

func tuningShow(cfg *config.Config) error {
	if !router.CustomTuning(cfg) {
		_, err := os.Stdout.Write(automodel.Catalog)
		return err
	}
	data, err := os.ReadFile(cfg.Catalog)
	if err != nil {
		return err
	}
	if catalog.IsWhole(data) {
		_, err = os.Stdout.Write(data)
		return err
	}
	merged, err := catalog.Merge(automodel.Catalog, data)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(merged)
	return err
}

var tuningLine = regexp.MustCompile(`(?m)^\s*tuning\s*=.*$`)

// tuningUse writes tuning = "<mode>" in the config file, in place.
func tuningUse(cfg *config.Config, mode string) error {
	path := cfg.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	line := fmt.Sprintf("tuning = %q", mode)
	var out string
	if tuningLine.Match(data) {
		out = tuningLine.ReplaceAllString(string(data), line)
	} else {
		out = line + "\n" + string(data)
		// Keep the header comment first.
		if lines := strings.SplitAfter(string(data), "\n"); len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
			out = lines[0] + line + "\n" + strings.Join(lines[1:], "")
		}
	}
	var probe map[string]any
	if _, err := toml.Decode(out, &probe); err != nil {
		return fmt.Errorf("config would not parse: %w", err)
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return err
	}
	if mode == "custom" {
		if _, err := os.Stat(cfg.Catalog); err != nil {
			fmt.Printf("tuning: custom, but %s doesn't exist yet: routing uses the default until it does (automodel tuning init)\n", cfg.Catalog)
			return nil
		}
		c := *cfg
		c.Tuning = mode
		if _, issues, err := router.LoadCatalog(&c); err != nil || len(issues.Errors()) > 0 {
			fmt.Printf("tuning: custom, but the file is invalid (%v%v): routing uses the default until it's fixed\n", err, issues.Errors())
			return nil
		}
	}
	fmt.Printf("tuning: %s (the proxy and hooks pick it up on their own)\n", mode)
	return nil
}

func tuningInit(cfg *config.Config, full, force bool) error {
	if _, err := os.Stat(cfg.Catalog); err == nil && !force {
		return fmt.Errorf("%s already exists (--force overwrites it)", cfg.Catalog)
	}
	body := automodel.Catalog
	if !full {
		var err error
		if body, err = tuningTemplate(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Catalog), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cfg.Catalog, body, 0o600); err != nil {
		return err
	}
	fmt.Printf("created %s\n", cfg.Catalog)
	fmt.Println("edit it, check it with automodel tuning diff and automodel eval --check, then: automodel tuning use custom")
	return nil
}

// tuningTemplate is a commented file of the usual changes, with the
// default's current values.
func tuningTemplate() ([]byte, error) {
	c, err := catalog.Parse(automodel.Catalog)
	if err != nil {
		return nil, err
	}
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	var b strings.Builder
	b.WriteString(`# automodel custom tuning, layered over automodel's default catalog.
# Only the keys you set here change; everything else follows the default,
# which is updated with every release. Uncomment and edit what you want.
#   automodel tuning diff        your changes, with the defaults
#   automodel tuning show        the catalog routing uses
#   automodel eval --check       measure it on the labeled cases
#   automodel tuning use custom  route with it (use default to go back)
# Every key of the default is tunable (automodel tuning show --default).

`)
	m := c.Meta
	fmt.Fprintf(&b, "# The policy: working below the right tier costs this many times the cost\n# gap; how likely a prompt must be separate work (a new task, a wrap-up)\n# to go below the work in progress; how sure Jev must be that a prompt\n# asks for an effort, a mode or a model in words.\n# [meta]\n# underprovision_penalty = %v\n# relation_separate_threshold = %v\n# explicit_threshold = %v\n\n",
		m.UnderprovisionPenalty, m.RelationSeparateThreshold(), m.ExplicitThreshold())
	for _, t := range c.ScoredTiers(catalog.ScopeMain) {
		fmt.Fprintf(&b, "# What Jev reads for the main %s tier:\n# [tiers.main.%s]\n# criteria = %s\n\n", t.ID, t.ID, q(t.Criteria))
		break
	}
	for _, t := range c.TiersByRank(catalog.ScopeMain) {
		if t.Asked() {
			fmt.Fprintf(&b, "# The %s tier: its own question, and how sure Jev must be.\n# [tiers.main.%s]\n# question = %s\n# threshold = %v\n\n", t.ID, t.ID, q(t.Question), t.Threshold)
		}
	}
	for _, md := range c.ModesFor(catalog.ScopeMain) {
		fmt.Fprintf(&b, "# When %s (parallel agents) is worth it:\n# [modes.%s]\n# threshold = %v\n\n", md.ID, md.ID, md.Threshold)
	}
	lv := c.Questions.Level[catalog.ScopeMain]
	fmt.Fprintf(&b, "# The questions Jev answers:\n# [questions.level]\n# main = %s\n", q(lv))
	if r := c.Questions.Relation; r != nil {
		fmt.Fprintf(&b, "# [questions.relation]\n# question = %s\n", q(r.Question))
		if o := r.Options[catalog.RelationSideQuestion]; o != nil {
			fmt.Fprintf(&b, "# [questions.relation.options.%s]\n# what = %s\n", catalog.RelationSideQuestion, q(o.What))
		}
	}
	fmt.Fprintf(&b, "\n# What Jev sees of the conversation:\n# [state]\n# recent_prompts = %d\n# last_assistant_tokens = %d\n", c.State.RecentPromptsN(), c.State.LastAssistantN())
	return []byte(b.String()), nil
}
