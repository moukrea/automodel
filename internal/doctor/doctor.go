// Package doctor checks an automodel installation: one line per check, each
// problem with the command that fixes it.
package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/moukrea/automodel/internal/catalog"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/install"
	"github.com/moukrea/automodel/internal/proxy"
	"github.com/moukrea/automodel/internal/router"
	"github.com/moukrea/automodel/internal/update"
)

type Status string

const (
	OK   Status = "✓"
	Warn Status = "!"
	Fail Status = "✗"
)

type Result struct {
	Status Status
	Name   string
	Detail string
	Fix    string // a command or action, for Warn and Fail
}

// KeyURL returns the key's limits and usage; it spends no credits.
const KeyURL = "https://openrouter.ai/api/v1/key"

// Env is what the checks look at; tests point it at temp dirs and fake
// servers.
type Env struct {
	Version   string
	UpdateCmd string // how this install upgrades: "automodel update", "brew upgrade automodel"…
	Cfg       *config.Config
	Install   install.Options
	Client    *http.Client
	KeyURL    string
	// Latest returns the latest release tag; nil skips the check.
	Latest func(context.Context) (string, error)
}

// Run runs every check.
func Run(e Env) []Result {
	if e.Client == nil {
		e.Client = &http.Client{Timeout: 15 * time.Second}
	}
	rs := []Result{e.binary(), e.config(), e.key(), e.catalog(), e.proxy(), e.service()}
	rs = append(rs, e.settings()...)
	return append(rs, e.openRouter(), e.ledger())
}

// Print writes one line per result and returns how many failed.
func Print(w io.Writer, rs []Result) (failed int) {
	for _, r := range rs {
		line := fmt.Sprintf("%s %-13s %s", r.Status, r.Name, r.Detail)
		if r.Fix != "" && r.Status != OK {
			line += " → " + r.Fix
		}
		fmt.Fprintln(w, line)
		if r.Status == Fail {
			failed++
		}
	}
	return failed
}

func (e Env) binary() Result {
	r := Result{Status: OK, Name: "binary", Detail: fmt.Sprintf("automodel %s (%s)", e.Version, e.Install.Exe)}
	if e.Latest == nil {
		return r
	}
	if e.Version == "dev" {
		r.Status, r.Detail = Warn, r.Detail+", development build: no update check"
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second) // a slow resolver takes 5 s
	defer cancel()
	tag, err := e.Latest(ctx)
	switch {
	case err != nil:
		r.Status, r.Detail = Warn, r.Detail+", update check failed: "+err.Error()
	case update.Newer(e.Version, tag):
		r.Status, r.Detail, r.Fix = Warn, r.Detail+", "+tag+" available", e.UpdateCmd
	default:
		r.Detail += ", up to date"
	}
	return r
}

func (e Env) config() Result {
	p := e.Cfg.Path()
	if _, err := os.Stat(p); err != nil {
		return Result{Fail, "config", p + " missing", "automodel install"}
	}
	return Result{OK, "config", p, ""}
}

func (e Env) key() Result {
	switch {
	case os.Getenv("OPENROUTER_API_KEY") != "":
		return Result{OK, "key", "set ($OPENROUTER_API_KEY)", ""}
	case e.Cfg.OpenRouterAPIKey == "":
		return Result{Warn, "key", "missing: every decision uses the default tier", "automodel key set"}
	case e.Cfg.KeyFileTooOpen():
		return Result{Warn, "key", "set, but " + e.Cfg.Path() + " is readable by others", "chmod 600 " + e.Cfg.Path()}
	}
	return Result{OK, "key", "set (config file)", ""}
}

func (e Env) catalog() Result {
	store := router.NewStore(e.Cfg)
	c, err := store.Get()
	if err != nil {
		return Result{Fail, "catalog", err.Error(), "automodel install"}
	}
	issues := c.Validate(time.Now(), e.Cfg.StaleDays)
	if len(issues.Errors()) > 0 {
		return Result{Fail, "catalog", fmt.Sprintf("%s: %d error(s)", store.Source(), len(issues.Errors())), "automodel catalog check"}
	}
	d := "tuning " + store.Source()
	if router.CustomTuning(e.Cfg) {
		// A custom file that doesn't load leaves routing on the default: say so.
		data, rerr := os.ReadFile(e.Cfg.Catalog)
		if rerr != nil {
			return Result{Warn, "catalog", "tuning custom, but " + e.Cfg.Catalog + " is missing: routing uses the default", "automodel tuning init, or automodel tuning use default"}
		}
		merged, merr := catalog.Merge(catalog.Shipped, data)
		if catalog.Shipped == nil || catalog.IsWhole(data) {
			merged, merr = data, nil
		}
		cc, perr := catalog.Parse(merged)
		if merr != nil || perr != nil || len(cc.Validate(time.Now(), e.Cfg.StaleDays).Errors()) > 0 {
			return Result{Warn, "catalog", "tuning custom, but " + e.Cfg.Catalog + " is invalid: routing uses the default", "automodel catalog check"}
		}
		if ov, err := catalog.Overrides(catalog.Shipped, data); err == nil && catalog.Shipped != nil {
			d = fmt.Sprintf("tuning custom: %s (%d change(s) from the default)", e.Cfg.Catalog, len(ov))
		}
	} else {
		d = "tuning default (automodel's, updated with every release)"
	}
	if n := len(issues.Warnings()); n > 0 {
		d += fmt.Sprintf("; %d warning(s): automodel catalog check", n)
	}
	return Result{OK, "catalog", d, ""}
}

func (e Env) proxy() Result {
	start := "automodel start"
	resp, err := e.Client.Get("http://" + e.Cfg.Listen + proxy.HealthPath)
	if err != nil {
		return Result{Fail, "proxy", "not answering on " + e.Cfg.Listen + " (Claude Code can't reach the API)", start}
	}
	defer resp.Body.Close()
	var h struct{ Version string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil {
		return Result{Warn, "proxy", "listening on " + e.Cfg.Listen + ", version unknown (older proxy?)", "automodel install"}
	}
	if h.Version != e.Version {
		return Result{Warn, "proxy", fmt.Sprintf("listening on %s, runs %s (binary is %s)", e.Cfg.Listen, h.Version, e.Version), "automodel install"}
	}
	return Result{OK, "proxy", "listening on " + e.Cfg.Listen + ", " + h.Version, ""}
}

func (e Env) service() Result {
	mode, detail, ok := install.ServiceStatus(e.Install, e.Cfg)
	if !ok {
		fix := "automodel install"
		if mode == install.Detached {
			fix = e.Install.StartCmd()
		}
		return Result{Fail, "service", mode + ": " + detail, fix}
	}
	if mode == install.Detached {
		return Result{Warn, "service", mode + ": " + detail, "run `" + e.Install.StartCmd() + "` at login"}
	}
	return Result{OK, "service", mode + ": " + detail, ""}
}

func (e Env) settings() []Result {
	fix := "automodel install"
	r, err := install.InspectSettings(e.Install, e.Cfg)
	if err != nil {
		return []Result{{Fail, "settings", err.Error(), fix}}
	}
	out := []Result{{OK, "settings", e.Install.SettingsPath, ""}}
	want := "http://" + e.Cfg.Listen
	if got := r.Env["ANTHROPIC_BASE_URL"]; got != want {
		out = append(out, Result{Fail, "base URL", fmt.Sprintf("ANTHROPIC_BASE_URL is %q, not the proxy (%s)", got, want), fix})
	} else {
		out = append(out, Result{OK, "base URL", "ANTHROPIC_BASE_URL → " + want, ""})
	}
	if got := r.Env["ANTHROPIC_CUSTOM_MODEL_OPTION"]; got != e.Cfg.CustomModelID {
		out = append(out, Result{Fail, "model option", fmt.Sprintf("ANTHROPIC_CUSTOM_MODEL_OPTION is %q, want %q", got, e.Cfg.CustomModelID), fix})
	} else {
		out = append(out, Result{OK, "model option", fmt.Sprintf("%q offered in /model", e.Cfg.CustomModelID), ""})
	}
	var env []string
	for _, k := range r.BadEnv {
		if k != "ANTHROPIC_BASE_URL" && k != "ANTHROPIC_CUSTOM_MODEL_OPTION" {
			env = append(env, k)
		}
	}
	out = append(out, list("env", "entries in place", "missing or changed: ", env, fix))
	out = append(out, list("hooks", "all in place", "missing or for another binary: ", r.Hooks, fix))
	switch r.StatuslineBy {
	case "automodel":
		out = append(out, Result{OK, "statusline", "automodel segment set", ""})
	case "agentline":
		out = append(out, Result{OK, "statusline", "agentline shows the automodel segment", ""})
	default:
		out = append(out, Result{Fail, "statusline", "not the automodel statusline", fix})
	}
	return out
}

func list(name, ok, bad string, items []string, fix string) Result {
	if len(items) == 0 {
		return Result{OK, name, ok, ""}
	}
	return Result{Fail, name, bad + strings.Join(items, ", "), fix}
}

// openRouter checks the key against OpenRouter's key endpoint (free).
func (e Env) openRouter() Result {
	key := e.Cfg.APIKey()
	if key == "" {
		return Result{Warn, "openrouter", "skipped: no key", "automodel key set"}
	}
	url := e.KeyURL
	if url == "" {
		url = KeyURL
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return Result{Fail, "openrouter", err.Error(), ""}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := e.Client.Do(req)
	if err != nil {
		return Result{Fail, "openrouter", "unreachable: " + err.Error(), "check your network or proxy settings"}
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Result{Fail, "openrouter", "key rejected (" + resp.Status + ")", "automodel key set"}
	case resp.StatusCode != http.StatusOK:
		return Result{Fail, "openrouter", "key check answered " + resp.Status, "retry later"}
	}
	var k struct {
		Data struct {
			LimitRemaining *float64 `json:"limit_remaining"`
		} `json:"data"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&k)
	d := "reachable, key accepted"
	if r := k.Data.LimitRemaining; r != nil {
		d += fmt.Sprintf(", $%.2f left on its limit", *r)
		if *r <= 0 {
			return Result{Fail, "openrouter", d, "raise the key's limit or add credits on openrouter.ai"}
		}
	}
	return Result{OK, "openrouter", d, ""}
}

func (e Env) ledger() Result {
	p := e.Cfg.Ledger
	fix := "make " + filepath.Dir(p) + " writable"
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return Result{Fail, "ledger", err.Error(), fix}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{Fail, "ledger", err.Error(), fix}
	}
	f.Close()
	return Result{OK, "ledger", p + " writable", ""}
}
