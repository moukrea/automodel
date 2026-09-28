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
	case d.Skipped:
		fmt.Fprintf(w, "  → keeps %s without asking Jev: %s\n", result, d.KeepReason)
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
