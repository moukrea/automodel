// Package config holds the router's runtime configuration (behaviour only;
// everything about models lives in the catalog).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BurntSushi/toml"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	d.Duration = v
	return err
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

type Config struct {
	Listen        string `toml:"listen"`
	Upstream      string `toml:"upstream"`
	CustomModelID string `toml:"custom_model_id"`
	Catalog       string `toml:"catalog"`
	// Tuning picks the catalog automodel routes with: "default" (shipped
	// in the binary, updated with every release) or "custom" (the file at
	// Catalog, layered over the default). Unset (configs from before
	// 0.12): custom if that file was edited, else default.
	Tuning string `toml:"tuning"`
	// CatalogExact (flags only): Catalog is a whole catalog, used alone.
	CatalogExact bool `toml:"-"`
	// NoLastGood (flags only): the catalog is a candidate (eval --catalog),
	// never kept as the hooks' last valid catalog.
	NoLastGood                   bool     `toml:"-"`
	StateDir                     string   `toml:"state_dir"`
	Ledger                       string   `toml:"ledger"` // default: <state_dir>/ledger.jsonl
	CacheTTL                     Duration `toml:"cache_ttl"`
	JevURL                       string   `toml:"jev_url"`
	JevTimeout                   Duration `toml:"jev_timeout"`
	StateBudgetTokens            int      `toml:"state_budget_tokens"`
	ThetaAct                     float64  `toml:"theta_act"`
	ThetaLow                     float64  `toml:"theta_low"`
	RespectExplicitSubagentModel bool     `toml:"respect_explicit_subagent_model"`
	JevShadowModel               string   `toml:"jev_shadow_model"`
	StaleDays                    int      `toml:"stale_days"`
	RepoPolicyFile               string   `toml:"repo_policy_file"`
	// Privacy is what the routing state sent to Jev may contain: "full"
	// (default) or "metadata" (sizes and task-kind hints, no text).
	Privacy string `toml:"privacy"`
	// RecordStates keeps the routing state of each decision locally
	// (<state_dir>/states), for `automodel flag`.
	RecordStates       bool     `toml:"record_states"`
	StatuslineFlash    Duration `toml:"statusline_flash"`
	StatuslineCommand  string   `toml:"statusline_command"`
	RouteWorkflowSteps bool     `toml:"route_workflow_steps"`
	// Features are the v2 behaviours; all off gives the v1 router (decisions
	// only when the cache is already lost, confidence-escalation policy).
	Features Features `toml:"features"`
	Update   Update   `toml:"update"`
	Budget   Budget   `toml:"budget"`
	// OpenRouterAPIKey is used when $OPENROUTER_API_KEY is unset. Keep the
	// file private (0600): automodel warns in hooks.log otherwise.
	OpenRouterAPIKey string `toml:"openrouter_api_key"`

	path string
}

// Update controls self-updates from GitHub releases.
type Update struct {
	// Auto lets the proxy service install new releases (checked every
	// Interval, applied when the proxy is idle; systemd restarts it).
	Auto     bool     `toml:"auto"`
	Interval Duration `toml:"interval"`
}

// Budget caps spending: once the day's (or a session's) spend reaches its
// cap, routing picks no tier above MaxTierWhenOver until the next day (or
// session). 0 turns a cap off. Pins are never capped.
type Budget struct {
	USDPerDay               float64 `toml:"usd_per_day"`
	USDPerSession           float64 `toml:"usd_per_session"`
	MaxTierWhenOver         string  `toml:"max_tier_when_over"`
	MaxSubagentTierWhenOver string  `toml:"max_subagent_tier_when_over"`
}

type Features struct {
	// WarmDecisions re-evaluates the tier on warm turns too, not only on
	// the first prompt, after a compaction or on a cold cache.
	WarmDecisions bool `toml:"warm_decisions"`
	// PerTurnEffort changes effort mid-conversation with effort-only system
	// messages, which keeps the prompt cache (models with per_turn_effort).
	PerTurnEffort bool `toml:"per_turn_effort"`
	// CostAware picks tiers by expected cost: the cost of working at a wrong
	// effort against the cost of switching (cache rebuild), and only switches
	// on a warm turn when the decision is confident enough.
	CostAware bool `toml:"cost_aware"`
	// FastPath brings back the work in progress's tier and mode without
	// asking Jev when the prompt is a bare go-ahead ("yes", "continue",
	// "oui, vas-y"): it continues that work.
	FastPath bool `toml:"fast_path"`

	// WarmMinConfidence is the confidence a warm downgrade that costs
	// something (a cache rebuild) needs; a free per-turn effort change only
	// answers to the work in progress (router.Judge).
	WarmMinConfidence float64 `toml:"warm_min_confidence"`
	// WarmTimeout bounds the Jev call on warm turns (on timeout nothing changes).
	// 4s like the initial call: on 2026-09-28 Jev's latency on OpenRouter went
	// from ~0.5s to 1.6-3.4s for hours, and 3s turned routine prompts into
	// fallbacks that keep the wrong effort for the whole turn.
	WarmTimeout Duration `toml:"warm_timeout"`
	// SwitchHorizonPrompts is how many prompts a switch is expected to serve
	// when weighing it against a cache rebuild.
	SwitchHorizonPrompts float64 `toml:"switch_horizon_prompts"`
}

func Default() *Config {
	state := filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "automodel")
	return &Config{
		Listen:                       "127.0.0.1:8788",
		Upstream:                     "https://api.anthropic.com",
		CustomModelID:                "jev",
		Catalog:                      "catalog.toml",
		StateDir:                     state,
		CacheTTL:                     Duration{time.Hour},
		JevURL:                       "https://openrouter.ai/api/alpha/decisions",
		JevTimeout:                   Duration{4 * time.Second},
		StateBudgetTokens:            24000,
		ThetaAct:                     0.6,
		ThetaLow:                     0.35,
		RespectExplicitSubagentModel: true,
		StaleDays:                    60,
		RepoPolicyFile:               ".automodel.toml",
		StatuslineFlash:              Duration{30 * time.Second},
		RouteWorkflowSteps:           true,
		RecordStates:                 true,
		Update:                       Update{Auto: true, Interval: Duration{6 * time.Hour}},
		Budget:                       Budget{MaxTierWhenOver: "medium", MaxSubagentTierWhenOver: "opus-medium"},
		Features: Features{
			WarmDecisions: true, PerTurnEffort: true, CostAware: true, FastPath: true,
			WarmMinConfidence: 0.8, WarmTimeout: Duration{4 * time.Second}, SwitchHorizonPrompts: 3,
		},
	}
}

// DefaultPath is $AUTOMODEL_CONFIG, else $XDG_CONFIG_HOME/automodel/config.toml.
func DefaultPath() string {
	if p := os.Getenv("AUTOMODEL_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "automodel", "config.toml")
}

// Load reads a config file over the defaults. A missing file at the default
// path is not an error. Relative paths resolve against the file's directory.
// retiredKeys are accepted and ignored so older config files keep loading.
var retiredKeys = map[string]bool{
	// Routing only ever applies to sessions positively on the custom model.
	"decide_when_model_unknown": true,
}

func Load(path string) (*Config, error) {
	c := Default()
	explicit := path != ""
	if !explicit {
		path = DefaultPath()
	}
	c.path = path
	md, err := toml.DecodeFile(path, c)
	switch {
	case err == nil:
		var unknown []toml.Key
		for _, k := range md.Undecoded() {
			if !retiredKeys[k.String()] {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("%s: unknown keys %v", path, unknown)
		}
	case os.IsNotExist(err) && !explicit:
	default:
		return nil, err
	}
	base := filepath.Dir(path)
	c.Catalog = resolve(base, c.Catalog)
	c.StateDir = resolve(base, expand(c.StateDir))
	if c.Ledger == "" {
		c.Ledger = filepath.Join(c.StateDir, "ledger.jsonl")
	}
	c.Ledger = resolve(base, expand(c.Ledger))
	if c.StatuslineCommand != "" {
		c.StatuslineCommand = expand(c.StatuslineCommand)
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) Path() string { return c.path }

// APIKey returns the OpenRouter key: environment first, then the config file.
func (c *Config) APIKey() string {
	if k := os.Getenv("OPENROUTER_API_KEY"); k != "" {
		return k
	}
	return c.OpenRouterAPIKey
}

// KeyFileTooOpen reports a config file holding a key that others can read.
func (c *Config) KeyFileTooOpen() bool {
	// Windows has no mode bits (Perm is always 0666 or 0444): the file is
	// in the user's profile, which its ACL keeps private.
	if c.OpenRouterAPIKey == "" || c.path == "" || runtime.GOOS == "windows" {
		return false
	}
	st, err := os.Stat(c.path)
	return err == nil && st.Mode().Perm()&0o077 != 0
}

// LastGoodCatalog is where the last valid catalog is kept for hook processes.
func (c *Config) LastGoodCatalog() string { return filepath.Join(c.StateDir, "catalog.lastgood.toml") }

func (c *Config) check() error {
	switch {
	case c.CustomModelID == "":
		return fmt.Errorf("custom_model_id is empty")
	case c.ThetaLow > c.ThetaAct:
		return fmt.Errorf("theta_low (%v) must be <= theta_act (%v)", c.ThetaLow, c.ThetaAct)
	case c.Features.WarmMinConfidence < 0 || c.Features.WarmMinConfidence > 1:
		return fmt.Errorf("features.warm_min_confidence must be in [0, 1]")
	case c.Features.SwitchHorizonPrompts <= 0:
		return fmt.Errorf("features.switch_horizon_prompts must be > 0")
	case c.Budget.USDPerDay < 0 || c.Budget.USDPerSession < 0:
		return fmt.Errorf("budget caps must be >= 0 (0 = off)")
	case c.Tuning != "" && c.Tuning != "default" && c.Tuning != "custom":
		return fmt.Errorf("tuning must be \"default\" or \"custom\", not %q", c.Tuning)
	case c.StateBudgetTokens <= 0 || c.StateBudgetTokens > 30000:
		return fmt.Errorf("state_budget_tokens must be in (0, 30000] (Jev limit is 32k for state + question)")
	}
	return nil
}

func resolve(base, p string) string {
	p = expand(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}

func expand(p string) string {
	if len(p) >= 2 && p[:2] == "~/" {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, fallback)
}
