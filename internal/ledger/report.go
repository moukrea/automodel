package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type TokenTotals struct {
	Requests      int `json:"requests"`
	Input         int `json:"input_tokens"`
	Output        int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

func (t *TokenTotals) add(u Usage) {
	t.Requests++
	t.Input += u.InputTokens
	t.Output += u.OutputTokens
	t.CacheRead += u.CacheReadInputTokens
	t.CacheCreation += u.CacheCreationInputTokens
}

// CacheHitRate is cache reads over all prompt tokens.
func (t TokenTotals) CacheHitRate() float64 {
	total := t.Input + t.CacheRead + t.CacheCreation
	if total == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(total)
}

type ScopeStats struct {
	Decisions    int            `json:"decisions"`
	Tiers        map[string]int `json:"tiers"`
	Triggers     map[string]int `json:"triggers"`
	Fallbacks    int            `json:"fallbacks"`
	FallbackRate float64        `json:"fallback_rate"`
	Escalated    int            `json:"escalated"` // chosen != Jev's first choice
	MeanConf     float64        `json:"mean_confidence"`
	ConfBuckets  map[string]int `json:"confidence_buckets"`
	Modes        map[string]int `json:"modes,omitempty"`
	Warm         *WarmStats     `json:"warm,omitempty"`
}

// WarmStats covers warm-turn evaluations (features.warm_decisions).
type WarmStats struct {
	Evaluated  int            `json:"evaluated"`
	Skipped    int            `json:"skipped"` // Jev not asked: no switch could pay back
	Switched   int            `json:"switched"`
	Kept       map[string]int `json:"kept"` // reason -> count
	Transition map[string]int `json:"transitions"`
	SwitchUSD  float64        `json:"switch_cost_usd"`
	GainUSD    float64        `json:"expected_gain_usd"`
}

type SessionStats struct {
	SessionID    string      `json:"session_id"`
	Routed       bool        `json:"routed"`
	Tokens       TokenTotals `json:"tokens"`
	CacheHitRate float64     `json:"cache_hit_rate"`
}

type ShadowStats struct {
	Model        string         `json:"model"`
	Compared     int            `json:"compared"`
	Agree        int            `json:"agree"`
	AgreeRate    float64        `json:"agree_rate"`
	MeanConf     float64        `json:"mean_confidence"`
	Errors       int            `json:"errors"`
	Disagreement map[string]int `json:"disagreements"` // "old→new"
}

type Report struct {
	Since            time.Time               `json:"since,omitzero"`
	Scopes           map[string]*ScopeStats  `json:"scopes"`
	JevCostUSD       float64                 `json:"jev_cost_usd"`
	TokensByTier     map[string]*TokenTotals `json:"tokens_by_tier"`
	Sessions         []SessionStats          `json:"sessions"`
	RoutedCacheHit   float64                 `json:"routed_cache_hit_rate"`
	UnroutedCacheHit float64                 `json:"unrouted_cache_hit_rate"`
	Shadow           *ShadowStats            `json:"shadow,omitempty"`
	Savings          *Savings                `json:"savings,omitempty"`
	Suggestions      []Suggestion            `json:"suggestions,omitempty"`
	Budget           *BudgetStats            `json:"budget,omitempty"`
}

// BudgetStats covers the spending cap ([budget]).
type BudgetStats struct {
	Capped        int     `json:"capped_decisions"`
	TodayUSD      float64 `json:"today_usd"`
	USDPerDay     float64 `json:"usd_per_day,omitempty"`
	USDPerSession float64 `json:"usd_per_session,omitempty"`
}

// BuildReport aggregates ledger lines newer than since.
func BuildReport(r io.Reader, since time.Time) (*Report, error) {
	rep := &Report{Since: since, Scopes: map[string]*ScopeStats{}, TokensByTier: map[string]*TokenTotals{}}
	sessions := map[string]*SessionStats{}
	confSum := map[string]float64{}
	var routed, unrouted TokenTotals
	var shConf float64
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var head struct {
			Kind string    `json:"kind"`
			TS   time.Time `json:"ts"`
		}
		line := sc.Bytes()
		if json.Unmarshal(line, &head) != nil || head.TS.Before(since) {
			continue
		}
		switch head.Kind {
		case "decision":
			var d Decision
			if json.Unmarshal(line, &d) != nil {
				continue
			}
			s := rep.Scopes[d.Scope]
			if s == nil {
				s = &ScopeStats{Tiers: map[string]int{}, Triggers: map[string]int{}, ConfBuckets: map[string]int{}}
				rep.Scopes[d.Scope] = s
			}
			rep.JevCostUSD += d.JevCostUSD
			if d.BudgetCap != "" {
				if rep.Budget == nil {
					rep.Budget = &BudgetStats{}
				}
				rep.Budget.Capped++
			}
			if d.Warm {
				w := s.Warm
				if w == nil {
					w = &WarmStats{Kept: map[string]int{}, Transition: map[string]int{}}
					s.Warm = w
				}
				w.Evaluated++
				switch {
				case d.Skipped && d.Kept:
					w.Skipped++
				case d.Kept:
					w.Kept[d.KeepReason]++
				default: // a go-ahead that carried the work on (skipped) switched too

					w.Switched++
					w.Transition[d.From+"→"+d.Chosen]++
					w.SwitchUSD += d.SwitchUSD
					w.GainUSD += d.GainUSD
				}
				if d.Kept {
					continue // a kept warm turn is not a new decision
				}
			}
			s.Decisions++
			s.Tiers[d.Chosen]++
			s.Triggers[d.Trigger]++
			if d.Mode != "" {
				if s.Modes == nil {
					s.Modes = map[string]int{}
				}
				s.Modes[d.Mode]++
			}
			if d.Trigger == "fallback" {
				s.Fallbacks++
			} else {
				confSum[d.Scope] += d.Confidence
				s.ConfBuckets[bucket(d.Confidence)]++
				if d.JevChoice != "" && d.JevChoice != d.Chosen {
					s.Escalated++
				}
			}
			if sh := d.Shadow; sh != nil {
				if rep.Shadow == nil {
					rep.Shadow = &ShadowStats{Model: sh.Model, Disagreement: map[string]int{}}
				}
				rep.JevCostUSD += sh.CostUSD
				if sh.Error != "" || d.Trigger == "fallback" {
					rep.Shadow.Errors++
					continue
				}
				rep.Shadow.Compared++
				shConf += sh.Confidence
				if sh.Chosen == d.Chosen {
					rep.Shadow.Agree++
				} else {
					rep.Shadow.Disagreement[d.Chosen+"→"+sh.Chosen]++
				}
			}
		case "usage":
			var u Usage
			if json.Unmarshal(line, &u) != nil {
				continue
			}
			tier := u.Tier
			if tier == "" {
				tier = "(unrouted) " + u.Model
			} else {
				tier = u.Scope + "/" + tier
			}
			tt := rep.TokensByTier[tier]
			if tt == nil {
				tt = &TokenTotals{}
				rep.TokensByTier[tier] = tt
			}
			tt.add(u)
			ss := sessions[u.SessionID]
			if ss == nil {
				ss = &SessionStats{SessionID: u.SessionID}
				sessions[u.SessionID] = ss
			}
			if u.Scope == "main" {
				ss.Routed = ss.Routed || u.Routed
				ss.Tokens.add(u)
			}
		}
	}
	for scope, s := range rep.Scopes {
		if s.Decisions > 0 {
			s.FallbackRate = float64(s.Fallbacks) / float64(s.Decisions)
		}
		if n := s.Decisions - s.Fallbacks; n > 0 {
			s.MeanConf = confSum[scope] / float64(n)
		}
	}
	if sh := rep.Shadow; sh != nil && sh.Compared > 0 {
		sh.AgreeRate = float64(sh.Agree) / float64(sh.Compared)
		sh.MeanConf = shConf / float64(sh.Compared)
	}
	for _, ss := range sessions {
		if ss.Tokens.Requests == 0 {
			continue
		}
		ss.CacheHitRate = ss.Tokens.CacheHitRate()
		if ss.Routed {
			addTotals(&routed, ss.Tokens)
		} else {
			addTotals(&unrouted, ss.Tokens)
		}
		rep.Sessions = append(rep.Sessions, *ss)
	}
	sort.Slice(rep.Sessions, func(i, j int) bool { return rep.Sessions[i].SessionID < rep.Sessions[j].SessionID })
	rep.RoutedCacheHit = routed.CacheHitRate()
	rep.UnroutedCacheHit = unrouted.CacheHitRate()
	return rep, sc.Err()
}

func addTotals(dst *TokenTotals, t TokenTotals) {
	dst.Requests += t.Requests
	dst.Input += t.Input
	dst.Output += t.Output
	dst.CacheRead += t.CacheRead
	dst.CacheCreation += t.CacheCreation
}

func bucket(c float64) string {
	switch {
	case c < 0.35:
		return "<0.35"
	case c < 0.6:
		return "0.35-0.6"
	case c < 0.8:
		return "0.6-0.8"
	default:
		return ">=0.8"
	}
}

func ReportFile(path string, since time.Time) (*Report, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return BuildReport(strings.NewReader(""), since)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return BuildReport(f, since)
}

// Markdown renders the report for humans.
func (rep *Report) Markdown(w io.Writer) {
	fmt.Fprintln(w, "# automodel report")
	if !rep.Since.IsZero() {
		fmt.Fprintf(w, "\nSince %s.\n", rep.Since.Format(time.RFC3339))
	}
	for _, scope := range sortedKeys(rep.Scopes) {
		s := rep.Scopes[scope]
		fmt.Fprintf(w, "\n## Decisions — %s\n\n", scope)
		fmt.Fprintf(w, "%d decisions, fallback rate %.1f%%, mean confidence %.2f, escalated by policy %d\n\n",
			s.Decisions, 100*s.FallbackRate, s.MeanConf, s.Escalated)
		fmt.Fprintln(w, "| Tier | Count | Share |\n|---|---:|---:|")
		for _, t := range sortedKeys(s.Tiers) {
			fmt.Fprintf(w, "| %s | %d | %.1f%% |\n", t, s.Tiers[t], 100*float64(s.Tiers[t])/float64(s.Decisions))
		}
		fmt.Fprintf(w, "\nTriggers: %s\n", kv(s.Triggers))
		fmt.Fprintf(w, "Confidence: %s\n", kv(s.ConfBuckets))
		if len(s.Modes) > 0 {
			fmt.Fprintf(w, "Modes: %s\n", kv(s.Modes))
		}
		if ws := s.Warm; ws != nil {
			fmt.Fprintf(w, "\nWarm turns: %d evaluated, %d switched, %d skipped (no switch could pay back)\n", ws.Evaluated, ws.Switched, ws.Skipped)
			if len(ws.Kept) > 0 {
				fmt.Fprintf(w, "Kept: %s\n", kv(ws.Kept))
			}
			if len(ws.Transition) > 0 {
				fmt.Fprintf(w, "Switches: %s (switch cost $%.2f, expected gain $%.2f)\n", kv(ws.Transition), ws.SwitchUSD, ws.GainUSD)
			}
		}
	}
	fmt.Fprintf(w, "\n## Tokens by tier\n\n| Tier | Requests | Input | Output | Cache read | Cache write | Hit rate |\n|---|---:|---:|---:|---:|---:|---:|\n")
	for _, t := range sortedKeys(rep.TokensByTier) {
		tt := rep.TokensByTier[t]
		fmt.Fprintf(w, "| %s | %d | %d | %d | %d | %d | %.1f%% |\n", t, tt.Requests, tt.Input, tt.Output, tt.CacheRead, tt.CacheCreation, 100*tt.CacheHitRate())
	}
	fmt.Fprintf(w, "\n## Cache\n\nMain-thread cache hit rate: routed %.1f%%, unrouted %.1f%% (routed must not be lower).\n",
		100*rep.RoutedCacheHit, 100*rep.UnroutedCacheHit)
	fmt.Fprintf(w, "\n## Jev\n\nCumulative cost: $%.4f\n", rep.JevCostUSD)
	if sh := rep.Shadow; sh != nil {
		fmt.Fprintf(w, "\nShadow %s: %d compared, agreement %.1f%%, mean confidence %.2f, errors %d\n",
			sh.Model, sh.Compared, 100*sh.AgreeRate, sh.MeanConf, sh.Errors)
		if len(sh.Disagreement) > 0 {
			fmt.Fprintf(w, "Disagreements (applied→shadow): %s\n", kv(sh.Disagreement))
		}
	}
	if b := rep.Budget; b != nil {
		fmt.Fprintf(w, "\n## Budget\n\nToday $%.2f", b.TodayUSD)
		if b.USDPerDay > 0 {
			fmt.Fprintf(w, " of $%.2f per day", b.USDPerDay)
		}
		if b.USDPerSession > 0 {
			fmt.Fprintf(w, " (session cap $%.2f)", b.USDPerSession)
		}
		fmt.Fprintf(w, "; %d decisions lowered to the cap.\n", b.Capped)
	}
	writeSuggestions(w, rep.Suggestions)
}

func kv(m map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
