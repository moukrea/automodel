package ledger

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// WhyOptions selects what `automodel why` shows.
type WhyOptions struct {
	Session string // session ID or prefix; "" = the most recent session
	N       int    // decisions to show
	// Rank orders a scope's tiers (lowest first); unknown tiers sort last.
	Rank func(scope, tier string) int
}

// Decisions reads the decision records of path, oldest first.
func Decisions(path string) ([]Decision, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Decision
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line[:min(len(line), 80)]), `"kind":"decision"`) {
			continue
		}
		var d Decision
		if json.Unmarshal(line, &d) == nil {
			out = append(out, d)
		}
	}
	return out, sc.Err()
}

// SessionDecisions picks the decisions of one session (by ID prefix, or
// the session of the newest decision) and keeps the last n.
func SessionDecisions(all []Decision, session string, n int) (string, []Decision) {
	if session == "" && len(all) > 0 {
		session = all[len(all)-1].SessionID
	}
	// A prefix matching several sessions picks the most recent one.
	var sid string
	for i := len(all) - 1; i >= 0 && session != ""; i-- {
		if strings.HasPrefix(all[i].SessionID, session) {
			sid = all[i].SessionID
			break
		}
	}
	var out []Decision
	for _, d := range all {
		if sid != "" && d.SessionID == sid {
			out = append(out, d)
		}
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return sid, out
}

var apiDot = regexp.MustCompile(`-(\d+)-(\d+)(-\d{8})?$`)

// ShortModel turns an API model ID into a short label (opus-5.5).
func ShortModel(id string) string {
	return apiDot.ReplaceAllString(strings.TrimPrefix(id, "claude-"), "-$1.$2")
}

// WriteWhy renders decisions the way the demo's routing popup shows them.
func WriteWhy(w io.Writer, ds []Decision, o WhyOptions) {
	for i, d := range ds {
		if i > 0 {
			fmt.Fprintln(w)
		}
		writeOne(w, d, o)
	}
}

func writeOne(w io.Writer, d Decision, o WhyOptions) {
	what := d.Scope
	if d.AgentType != "" && d.AgentType != d.Trigger {
		what += " (" + d.AgentType + ")"
	}
	head := fmt.Sprintf("%s  %s · %s", d.TS.Local().Format("15:04:05"), what, d.Trigger)
	if d.Warm && d.Trigger == "warm" {
		head = fmt.Sprintf("%s  %s · warm turn", d.TS.Local().Format("15:04:05"), what)
	}
	if d.LatencyMS > 0 && !d.Skipped {
		head += fmt.Sprintf(" · Jev %.1f s", float64(d.LatencyMS)/1000)
	}
	fmt.Fprintln(w, head)

	result := ShortModel(d.Model)
	if d.Effort != "" {
		result += "·" + d.Effort
	}
	if d.Mode != "" {
		result += " +" + d.Mode
	}
	switch {
	case d.Trigger == "pinned":
		fmt.Fprintf(w, "  → %s, pinned by %s (routing paused until released)\n", result, d.Cause)
		return
	case d.Skipped && !d.Kept && d.Hold != "":
		fmt.Fprintf(w, "  → %s (was %s) without asking Jev: %s%s\n", result, d.From, d.Hold, goAheadWork(d))
		return
	case d.Skipped:
		fmt.Fprintf(w, "  → keeps %s without asking Jev: %s%s\n", result, d.KeepReason, goAheadWork(d))
		return
	case d.Trigger == "fallback":
		fmt.Fprintf(w, "  ⚠ Jev failed (%s): default tier %s\n", d.Error, result)
		return
	}

	tiers := make([]string, 0, len(d.Probs))
	for t := range d.Probs {
		tiers = append(tiers, t)
	}
	sort.SliceStable(tiers, func(i, j int) bool {
		if o.Rank != nil {
			if ri, rj := o.Rank(d.Scope, tiers[i]), o.Rank(d.Scope, tiers[j]); ri != rj {
				return ri < rj
			}
		}
		return tiers[i] < tiers[j]
	})
	width := 1
	for _, t := range tiers {
		width = max(width, len(t))
	}
	for _, t := range tiers {
		p := d.Probs[t]
		bar := strings.Repeat("█", int(p*24+0.5))
		if bar == "" {
			bar = "·"
		}
		mark := ""
		if t == d.Chosen {
			mark = "  ← pick"
			if d.Kept {
				mark = "  ← stays"
			}
		} else if t == d.JevChoice {
			mark = "  (Jev's top answer)"
		}
		fmt.Fprintf(w, "  %-*s  %-24s %.2f%s\n", width, t, bar, p, mark)
	}

	line := "  → " + result
	if d.Kept {
		line = "  → keeps " + result
	} else if d.From != "" && d.From != d.Chosen {
		line += " (was " + d.From + ")"
	}
	fmt.Fprintf(w, "%s · confidence %.2f\n", line, d.Confidence)

	var why []string
	if d.Signals["asks_more_thinking"] == true {
		why = append(why, "you asked for more thinking: one tier up at least")
	}
	if d.Signals["released_pin"] == true {
		why = append(why, "you handed the pick back ([effort:auto] / [model:auto])")
	}
	if d.Signals["previous_turn_interrupted"] == true {
		why = append(why, "the previous turn was interrupted")
	}
	if d.KeepReason != "" {
		why = append(why, d.KeepReason)
	}
	if d.BudgetCap != "" {
		why = append(why, "⚠ budget cap: "+d.BudgetCap+" → "+d.Chosen)
	}
	if d.ContinuesP != nil {
		why = append(why, fmt.Sprintf("continues the work: %.2f", *d.ContinuesP))
	}
	for id, p := range d.AskedP {
		why = append(why, fmt.Sprintf("%s: yes %.2f", id, p))
	}
	if d.InformsP != nil {
		why = append(why, fmt.Sprintf("only informs: %.2f", *d.InformsP))
	}
	if p, ok := d.ModeP[d.Mode]; ok && d.Mode != "" {
		why = append(why, fmt.Sprintf("%s: yes %.2f", d.Mode, p))
	}
	if d.Warm && !d.Kept {
		why = append(why, fmt.Sprintf("switch cost $%.2f", d.SwitchUSD))
		if d.GainUSD > 0 {
			why = append(why, fmt.Sprintf("expected gain $%.2f", d.GainUSD))
		}
	}
	if len(why) > 0 {
		fmt.Fprintln(w, "  "+strings.Join(why, " · "))
	}
	if l := workLine(d, o); l != "" {
		fmt.Fprintln(w, "  "+l)
	}
}

// workLine says how the prompt relates to the work in progress, what it
// asked for in words, and why the tier was held at the work's level or
// left to the prompt's own.
func workLine(d Decision, o WhyOptions) string {
	var parts []string
	if len(d.Relation) > 0 {
		parts = append(parts, "relation: "+ranked(d.Relation, 2))
	}
	if d.OfferP != nil {
		parts = append(parts, fmt.Sprintf("offered more of the detour: %.2f", *d.OfferP))
	}
	if len(d.Explicit) > 0 {
		parts = append(parts, "asks in words: "+ranked(d.Explicit, 3))
	}
	switch {
	case d.Hold != "" && d.WorkTier != "":
		parts = append(parts, d.Hold+" · work in progress "+d.WorkTier)
	case d.Hold != "":
		parts = append(parts, d.Hold)
	case d.WorkTier != "" && len(d.Relation) > 0:
		s := "separate from the work in progress (" + d.WorkTier + "): its own level"
		switch {
		case likeliest(d.Relation) == "aside":
			s = "an aside: its own level for this turn, the work in progress (" + d.WorkTier + ") unchanged"
		case d.WorkDone && d.Work == "":
			s = "the work in progress (" + d.WorkTier + ") was wrapped up: its own level"
		}
		if o.Rank != nil && o.Rank(d.Scope, d.Chosen) < o.Rank(d.Scope, d.WorkTier) {
			s += ", lower"
		}
		parts = append(parts, s)
	}
	switch d.Work {
	case "new":
		if d.Pauses && d.WorkTier != "" {
			parts = append(parts, "starts a new work in progress, pausing the one at "+d.WorkTier)
		} else {
			parts = append(parts, "starts a new work in progress")
		}
	case "set":
		parts = append(parts, "sets the work in progress")
	case "raised":
		parts = append(parts, "raises the work in progress")
	case "resumed":
		parts = append(parts, "resumes the paused work ("+d.PausedTier+")")
	case "done":
		parts = append(parts, "marks the work in progress done")
	case "reopened":
		parts = append(parts, "reopens the work in progress")
	}
	return strings.Join(parts, " · ")
}

// goAheadWork says what a go-ahead made of the work in progress.
func goAheadWork(d Decision) string {
	switch d.Work {
	case "resumed":
		return " (resumes the paused work)"
	case "reopened":
		return " (reopens the work in progress)"
	}
	return ""
}

// likeliest is the likeliest entry of a probability map.
func likeliest(p map[string]float64) string {
	best, bp := "", -1.0
	for k, v := range p {
		if v > bp || (v == bp && k < best) {
			best, bp = k, v
		}
	}
	return best
}

// ranked lists the n likeliest entries of a probability map ("extend 0.94,
// continue 0.04"), leaving out those under 0.01.
func ranked(p map[string]float64, n int) string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if p[keys[i]] != p[keys[j]] {
			return p[keys[i]] > p[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var out []string
	for i, k := range keys {
		if i >= n || (i > 0 && p[k] < 0.01) {
			break
		}
		out = append(out, fmt.Sprintf("%s %.2f", k, p[k]))
	}
	return strings.Join(out, ", ")
}

// FollowFrom prints the decisions of a session appended after the first
// seen ones, until stop is closed.
func FollowFrom(path, session string, seen int, o WhyOptions, w io.Writer, stop <-chan struct{}) error {
	for {
		all, err := Decisions(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		sid, ds := SessionDecisions(all, session, 0)
		if session == "" && sid != "" {
			session = sid
		}
		if len(ds) > seen {
			WriteWhy(w, ds[seen:], o)
			fmt.Fprintln(w)
			seen = len(ds)
		}
		select {
		case <-stop:
			return nil
		case <-time.After(time.Second):
		}
	}
}

// Nth returns the n-th latest decision (1 = the latest) of a session (ID
// or prefix; "" = the most recent) in a scope ("" = any).
func Nth(all []Decision, session, scope string, n int) (Decision, bool) {
	sid, ds := SessionDecisions(all, session, 0)
	for i := len(ds) - 1; i >= 0 && sid != ""; i-- {
		if scope != "" && ds[i].Scope != scope {
			continue
		}
		if n--; n <= 0 {
			return ds[i], true
		}
	}
	return Decision{}, false
}
