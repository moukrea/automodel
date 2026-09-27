// Command automodel routes Claude Code's "jev" model to a model and effort
// chosen by TypeSafe Jev: one binary for the proxy, the hooks and the
// statusline.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/moukrea/automodel"
	"github.com/moukrea/automodel/internal/eval"
	"github.com/moukrea/automodel/internal/update"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/doctor"
	"github.com/moukrea/automodel/internal/hooks"
	"github.com/moukrea/automodel/internal/install"
	"github.com/moukrea/automodel/internal/ledger"
	"github.com/moukrea/automodel/internal/proxy"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/state"
	"github.com/moukrea/automodel/internal/statusline"
)

var version = "dev"

const usage = `automodel — automatic model/effort routing for Claude Code via Jev

Usage:
  automodel serve                      run the proxy
  automodel hook <name>                run a hook (%s)
  automodel statusline                 render the statusline segment
  automodel report [--json] [--since 7d] [--baseline xhigh]
  automodel why [--session id] [-n 5] [--scope main] [--follow]   explain the latest routing decisions
  automodel catalog check [--json] [--catalog path]
  automodel eval [--catalog path] [--cases file] [--format score|choice] [--json]
                                       measure Jev's routing answers on labeled cases
  automodel install [--dry-run] [--catalog path] [--settings path]
                                       print (or apply) the Claude Code settings, config and service
  automodel uninstall                  remove the settings entries and the service
  automodel start                      start the proxy if it isn't running (at login, without systemd)
  automodel doctor                     check the installation (non-zero exit on a failed check)
  automodel update [--check]           install the latest release (the service also does it daily)
  automodel key set                    read the OpenRouter key on stdin into the config (0600)
  automodel prune [--older-than 30d]   drop old session state
  automodel version

Global flag: --config path (default $AUTOMODEL_CONFIG or ~/.config/automodel/config.toml)
`

func main() {
	args := os.Args[1:]
	cfgPath := ""
	if len(args) >= 2 && args[0] == "--config" {
		cfgPath, args = args[1], args[2:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, usage, strings.Join(hooks.Names(), ", "))
		os.Exit(2)
	}
	if err := run(cfgPath, args[0], args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "automodel:", err)
		os.Exit(1)
	}
}

func run(cfgPath, cmd string, args []string) error {
	switch cmd {
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		fmt.Printf(usage, strings.Join(hooks.Names(), ", "))
		return nil
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if cmd == "hook" || cmd == "statusline" {
			return nil // never break Claude Code
		}
		if cmd == "doctor" {
			doctor.Print(os.Stdout, []doctor.Result{{Status: doctor.Fail, Name: "config", Detail: err.Error(), Fix: "fix or remove the file, then automodel install"}})
			os.Exit(1)
		}
		return err
	}
	switch cmd {
	case "serve":
		return serve(cfg, args)
	case "hook":
		if len(args) != 1 {
			return fmt.Errorf("usage: automodel hook <%s>", strings.Join(hooks.Names(), "|"))
		}
		defer router.SetupLog(cfg.StateDir, "hooks.log")()
		env, err := router.New(cfg)
		if err != nil {
			log.Printf("hook %s: %v", args[0], err)
			io.Copy(io.Discard, os.Stdin)
			return nil
		}
		return hooks.Run(args[0], env, os.Stdin, os.Stdout)
	case "statusline":
		defer router.SetupLog(cfg.StateDir, "hooks.log")()
		env, err := router.New(cfg)
		if err != nil {
			log.Printf("statusline: %v", err)
			fmt.Println(cfg.CustomModelID + " → ⚠ catalog")
			return nil
		}
		return statusline.Run(env, os.Stdin, os.Stdout)
	case "report":
		return report(cfg, args)
	case "why":
		return why(cfg, args)
	case "catalog":
		return catalogCmd(cfg, args)
	case "eval":
		return evalCmd(cfg, args)
	case "install":
		return installCmd(cfg, args)
	case "update":
		return updateCmd(cfg, args)
	case "key":
		return keyCmd(cfg, args)
	case "uninstall":
		o, err := installOptions(cfg, nil)
		if err != nil {
			return err
		}
		return install.Remove(o)
	case "start":
		o, err := installOptions(cfg, nil)
		if err != nil {
			return err
		}
		return install.Start(o, cfg)
	case "doctor":
		return doctorCmd(cfg)
	case "prune":
		fs := flag.NewFlagSet("prune", flag.ExitOnError)
		older := fs.String("older-than", "30d", "age")
		fs.Parse(args)
		d, err := parseAge(*older)
		if err != nil {
			return err
		}
		n := state.Store{Dir: cfg.StateDir}.Prune(d)
		fmt.Printf("removed %d session files\n", n)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func serve(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	debug := fs.Bool("debug", os.Getenv("AUTOMODEL_DEBUG") != "", "log every rewrite")
	fs.Parse(args)
	store := &catalog.Store{Path: cfg.Catalog, LastGood: cfg.LastGoodCatalog(), StaleDays: cfg.StaleDays}
	cat, err := store.Get()
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	for _, w := range cat.Validate(time.Now(), cfg.StaleDays).Warnings() {
		log.Printf("catalog %s", w)
	}
	p, err := proxy.New(cfg, store)
	if err != nil {
		return err
	}
	p.Debug = *debug
	p.Version = version
	log.Printf("automodel %s listening on %s → %s (model %q, catalog %s)", version, cfg.Listen, cfg.Upstream, cfg.CustomModelID, cfg.Catalog)
	afterUpdate(cfg)
	exe := install.Self()
	switch {
	case upgradeCmd(exe) != "":
		go watchBinary(exe, p)
	case cfg.Update.Auto && version != "dev":
		go autoUpdate(cfg, p, exe)
	}
	srv := &http.Server{Addr: cfg.Listen, Handler: p, ReadHeaderTimeout: 30 * time.Second}
	return srv.ListenAndServe()
}

func evalCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	cases := fs.String("cases", "", "labeled cases (JSONL; default: the set shipped with this release)")
	format := fs.String("format", "score", "question format: score (the router's) or choice (v1)")
	asJSON := fs.Bool("json", false, "JSON output")
	parallel := fs.Int("parallel", 8, "concurrent Jev calls")
	catPath := fs.String("catalog", "", "catalog to evaluate (default: the configured one)")
	fs.Parse(args)
	if *catPath != "" {
		c := *cfg
		c.Catalog = *catPath
		cfg = &c
	}
	env, err := router.New(cfg)
	if err != nil {
		return err
	}
	var cs []eval.Case
	if *cases == "" {
		cs, err = eval.Parse(bytes.NewReader(automodel.EvalCases), "routing.jsonl (built in)")
	} else {
		cs, err = eval.Load(*cases)
	}
	if err != nil {
		return err
	}
	rs := eval.Run(context.Background(), env, cs, *format, *parallel)
	sum := eval.Summarize(env.Catalog, rs)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"results": rs, "summary": sum})
	}
	eval.Print(os.Stdout, env.Catalog, rs, sum)
	return nil
}

func report(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	since := fs.String("since", "", "only entries newer than this age (e.g. 7d, 12h)")
	ledgerPath := fs.String("ledger", cfg.Ledger, "ledger path")
	baseline := fs.String("baseline", "xhigh", "effort the savings estimate compares with (Claude Code's own setting)")
	fs.Parse(args)
	var from time.Time
	if *since != "" {
		d, err := parseAge(*since)
		if err != nil {
			return err
		}
		from = time.Now().Add(-d)
	}
	rep, err := ledger.ReportFile(*ledgerPath, from)
	if err != nil {
		return err
	}
	if c, _, err := catalog.Load(cfg.Catalog, time.Now(), 3650); err == nil {
		if sv, err := ledger.EstimateSavingsFile(*ledgerPath, from, c, c.DefaultTier(catalog.ScopeMain).Model, *baseline); err == nil && sv.Requests > 0 {
			rep.Savings = sv
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	rep.Markdown(os.Stdout)
	if rep.Savings != nil {
		rep.Savings.Markdown(os.Stdout)
	}
	return nil
}

func why(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	session := fs.String("session", "", "session ID or prefix (default: the most recent)")
	n := fs.Int("n", 5, "decisions to show")
	follow := fs.Bool("follow", false, "keep printing new decisions")
	scope := fs.String("scope", "", "only this scope: main or subagent")
	ledgerPath := fs.String("ledger", cfg.Ledger, "ledger path")
	fs.Parse(args)
	o := ledger.WhyOptions{Session: *session, N: *n}
	if c, _, err := catalog.Load(cfg.Catalog, time.Now(), 3650); err == nil {
		o.Rank = func(scope, tier string) int {
			if t := c.Tier(scope, tier); t != nil {
				return t.Rank
			}
			return 1 << 20
		}
	}
	all, err := ledger.Decisions(*ledgerPath)
	if err != nil {
		return err
	}
	if *scope != "" {
		kept := all[:0]
		for _, d := range all {
			if d.Scope == *scope {
				kept = append(kept, d)
			}
		}
		all = kept
	}
	sid, ds := ledger.SessionDecisions(all, *session, *n)
	if len(ds) == 0 {
		return errors.New("no decision recorded yet (is the session on Jev?)")
	}
	fmt.Printf("session %s · last %d decisions\n\n", sid[:min(8, len(sid))], len(ds))
	ledger.WriteWhy(os.Stdout, ds, o)
	if !*follow {
		return nil
	}
	fmt.Println()
	_, done := ledger.SessionDecisions(all, sid, 0)
	stop := make(chan struct{})
	return ledger.FollowFrom(*ledgerPath, sid, len(done), o, os.Stdout, stop)
}

func catalogCmd(cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] != "check" {
		return errors.New("usage: automodel catalog check [--json] [--catalog path]")
	}
	fs := flag.NewFlagSet("catalog check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	path := fs.String("catalog", cfg.Catalog, "catalog path")
	staleDays := fs.Int("stale-days", cfg.StaleDays, "warn about data older than this")
	fs.Parse(args[1:])
	_, issues, err := catalog.Load(*path, time.Now(), *staleDays)
	if err != nil {
		return err
	}
	if *asJSON {
		out := map[string]any{"catalog": *path, "valid": len(issues.Errors()) == 0,
			"errors": nonNil(issues.Errors()), "warnings": nonNil(issues.Warnings())}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
	} else {
		for _, i := range issues {
			fmt.Println(i)
		}
		fmt.Printf("%s: %d error(s), %d warning(s)\n", *path, len(issues.Errors()), len(issues.Warnings()))
	}
	if len(issues.Errors()) > 0 {
		os.Exit(1)
	}
	return nil
}

func nonNil(is catalog.Issues) catalog.Issues {
	if is == nil {
		return catalog.Issues{}
	}
	return is
}

func installOptions(cfg *config.Config, args []string) (install.Options, error) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	home, _ := os.UserHomeDir()
	cat := fs.String("catalog", cfg.Catalog, "catalog path written into a new config (default: the catalog shipped in the binary, copied next to the config)")
	settings := fs.String("settings", filepath.Join(home, ".claude", "settings.json"), "Claude Code settings file")
	fs.Bool("apply", true, "apply (the default; kept for compatibility)")
	dryRun := fs.Bool("dry-run", false, "print what would be merged instead of applying")
	fs.Parse(args)
	exe := install.Self()
	if exe == "" {
		return install.Options{}, errors.New("cannot find the automodel binary's path")
	}
	abs, _ := filepath.Abs(*cat)
	cfgPath := cfg.Path()
	if p, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = p
	}
	o := install.Options{
		Exe: exe, ConfigPath: cfgPath, CatalogPath: abs, SettingsPath: *settings,
		UnitPath: filepath.Join(home, ".config", "systemd", "user", "automodel.service"),
		Log:      func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	if *dryRun {
		o.Log = nil
	}
	return o, nil
}

func installCmd(cfg *config.Config, args []string) error {
	o, err := installOptions(cfg, args)
	if err != nil {
		return err
	}
	if o.Log == nil {
		return printInstall(o, cfg)
	}
	if seeded, err := install.SeedCatalog(o.CatalogPath, automodel.Catalog, cfg.StateDir); err != nil {
		return fmt.Errorf("catalog %s: %w", o.CatalogPath, err)
	} else if seeded {
		fmt.Printf("catalog written: %s\n", o.CatalogPath)
	}
	if err := install.Apply(o); err != nil {
		return err
	}
	recordInstalled(cfg)
	if os.Getenv("AUTOMODEL_INSTALLER") != "" {
		return nil // install.sh asks for the key and prints the next steps
	}
	fmt.Println("done: pick \"Jev (auto)\" in /model in a new Claude Code session.")
	if cfg2, err := config.Load(o.ConfigPath); err == nil && cfg2.APIKey() == "" {
		fmt.Printf("missing: openrouter_api_key in %s (until then decisions use the default tier)\n", o.ConfigPath)
	}
	return nil
}

func printInstall(o install.Options, cfg *config.Config) error {
	b, err := install.Preview(o, cfg)
	if err != nil {
		return err
	}
	fmt.Printf("# Entries merged into %s (config: %s)\n%s\n", o.SettingsPath, o.ConfigPath, b)
	fmt.Printf("# Service: %s runs `%s --config %s serve`\n", o.UnitPath, o.Exe, o.ConfigPath)
	return nil
}

func doctorCmd(cfg *config.Config) error {
	o, err := installOptions(cfg, nil)
	if err != nil {
		return err
	}
	up := upgradeCmd(o.Exe)
	if up == "" {
		up = "automodel update"
	}
	rs := doctor.Run(doctor.Env{Version: version, UpdateCmd: up, Cfg: cfg, Install: o,
		Latest: func(ctx context.Context) (string, error) {
			rel, err := update.Latest(ctx)
			if err != nil {
				return "", err
			}
			return rel.Tag, nil
		}})
	if n := doctor.Print(os.Stdout, rs); n > 0 {
		fmt.Printf("%d check(s) failed\n", n)
		os.Exit(1)
	}
	return nil
}

func parseAge(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil {
			return 0, fmt.Errorf("bad age %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// ---- releases

func installedMark(cfg *config.Config) string {
	return filepath.Join(cfg.StateDir, "installed-version")
}

func recordInstalled(cfg *config.Config) {
	os.MkdirAll(cfg.StateDir, 0o700)
	os.WriteFile(installedMark(cfg), []byte(version+"\n"), 0o600)
}

// afterUpdate runs once in the first proxy started on a new version: new
// releases may add settings entries, and the shipped catalog is refreshed
// while the user hasn't edited it.
func afterUpdate(cfg *config.Config) {
	prev, _ := os.ReadFile(installedMark(cfg))
	// A config that was never installed (a second proxy, a test instance)
	// must not touch Claude Code's settings: they belong to another install.
	if len(prev) == 0 || strings.TrimSpace(string(prev)) == version || version == "dev" {
		return
	}
	o, err := installOptions(cfg, nil)
	if err != nil {
		log.Printf("update refresh: %v", err)
		return
	}
	o.Log = func(f string, a ...any) { log.Printf(f, a...) }
	if seeded, err := install.SeedCatalog(cfg.Catalog, automodel.Catalog, cfg.StateDir); err != nil {
		log.Printf("update refresh: catalog: %v", err)
	} else if seeded {
		log.Printf("catalog updated to the one shipped with %s", version)
	}
	if err := install.Refresh(o, cfg); err != nil {
		log.Printf("update refresh: settings: %v", err)
	}
	recordInstalled(cfg)
	log.Printf("now running %s (was %s)", version, strings.TrimSpace(string(prev)))
}

// autoUpdate checks for a new release every cfg.Update.Interval, installs
// it, and restarts the proxy on it once idle.
func autoUpdate(cfg *config.Config, p *proxy.Proxy, exe string) {
	every := cfg.Update.Interval.Duration
	if every < time.Hour {
		every = time.Hour
	}
	time.Sleep(2 * time.Minute)
	for ; ; time.Sleep(every) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		rel, err := update.Latest(ctx)
		if err == nil && update.Newer(version, rel.Tag) {
			if err = update.Apply(ctx, rel, exe); err == nil {
				cancel()
				log.Printf("installed %s; restarting when idle", rel.Tag)
				restart(p, exe)
			}
		}
		cancel()
		if err != nil {
			log.Printf("auto-update: %v", err)
		}
	}
}

// watchBinary restarts the proxy once a package manager replaced exe.
func watchBinary(exe string, p *proxy.Proxy) {
	was, err := os.Stat(exe)
	if err != nil {
		return
	}
	for range time.Tick(time.Minute) {
		if now, err := os.Stat(exe); err == nil && (!os.SameFile(was, now) || !now.ModTime().Equal(was.ModTime())) {
			log.Printf("%s was upgraded; restarting when idle", exe)
			restart(p, exe)
		}
	}
}

// restart runs exe in place of the proxy once it is idle. Under systemd or
// launchd an exit is enough (they restart it); otherwise (a detached proxy,
// or `serve` run by hand) the process re-executes itself, keeping its pid so
// the pidfile stays right.
func restart(p *proxy.Proxy, exe string) {
	for !p.Idle(10 * time.Second) {
		time.Sleep(2 * time.Second)
	}
	switch os.Getenv(install.ServiceEnv) {
	case install.Systemd, install.Launchd:
		os.Exit(0)
	}
	err := syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
	log.Printf("restart on %s: %v", exe, err)
	os.Exit(1)
}

// upgradeCmd is how a package manager upgrades exe, or "" when automodel
// updates itself.
func upgradeCmd(exe string) string {
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	switch {
	case strings.Contains(exe, "/Cellar/"):
		return "brew upgrade automodel"
	case strings.HasPrefix(exe, "/usr/bin/"):
		if _, err := os.Stat("/var/lib/dpkg/info/automodel.list"); err == nil {
			return "sudo apt upgrade automodel"
		}
		return "sudo dnf upgrade automodel"
	}
	return ""
}

func updateCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "only report the latest version")
	fs.Parse(args)
	if up := upgradeCmd(install.Self()); up != "" && !*check {
		return fmt.Errorf("installed by a package manager: run `%s`", up)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rel, err := update.Latest(ctx)
	if err != nil {
		return err
	}
	if !update.Newer(version, rel.Tag) {
		fmt.Printf("automodel %s is up to date (latest %s)\n", version, rel.Tag)
		return nil
	}
	if *check {
		fmt.Printf("automodel %s → %s available\n", version, rel.Tag)
		return nil
	}
	exe := install.Self()
	if err := update.Apply(ctx, rel, exe); err != nil {
		return err
	}
	fmt.Printf("automodel %s → %s installed at %s\n", version, rel.Tag, exe)
	// The new binary re-applies the settings and restarts the service.
	cmd := exec.Command(exe, "--config", cfg.Path(), "install")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// keyCmd stores the OpenRouter key read from stdin in the config file,
// which stays 0600. The key is never printed.
func keyCmd(cfg *config.Config, args []string) error {
	if len(args) != 1 || args[0] != "set" {
		return errors.New("usage: automodel key set  (reads the key on stdin)")
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return errors.New("no key on stdin")
	}
	key := strings.TrimSpace(line)
	if key == "" || strings.ContainsAny(key, "\"\\ \t") {
		return errors.New("that doesn't look like an API key")
	}
	path := cfg.Path()
	data, _ := os.ReadFile(path)
	entry := fmt.Sprintf("openrouter_api_key = %q", key)
	lines := strings.Split(string(data), "\n")
	found := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "openrouter_api_key") {
			lines[i], found = entry, true
		}
	}
	out := strings.Join(lines, "\n")
	if !found {
		out = strings.TrimRight(out, "\n") + "\n" + entry + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		return err
	}
	os.Chmod(path, 0o600)
	fmt.Printf("key saved in %s\n", path)
	return nil
}
