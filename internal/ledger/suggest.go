package ledger

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Suggestion is a change the user's habits argue for, with the evidence.
type Suggestion struct {
	Repo   string `json:"repo,omitempty"`
	Text   string `json:"text"`
	Why    string `json:"why"`
	Events int    `json:"events"`
}

// SuggestOptions tunes Suggest.
type SuggestOptions struct {
	Rank      func(scope, tier string) int // tier order, lowest first
	Floor     string                       // main tier suggested as min_tier ("high")
	MinEvents int                          // evidence needed (default 5)
	// RepoFloor is a repository's current min_tier ("" when none).
	RepoFloor func(repo string) string
	// Flagged is the scope and wanted tier of each flagged case.
	Flagged [][2]string
}

type habits struct {
	pins, think, interrupts int
	pinned                  map[string]int
	sources                 map[string]int
}

// Suggest reads main-session habits per repository: pins above Jev's pick,
// "think harder" and interrupted turns while below the floor. Enough of
// them suggest a min_tier in that repo's .automodel.toml. Flagged cases
// wanting the same tier suggest reviewing its catalog criteria.
func Suggest(ds []Decision, o SuggestOptions) []Suggestion {
	if o.MinEvents <= 0 {
		o.MinEvents = 5
	}
	rank := func(t string) int { return o.Rank("main", t) }
	floor := rank(o.Floor)
	repos := map[string]*habits{}
	picks := map[string]string{} // session → Jev's pick in force
	for _, d := range ds {
		if d.Scope != "main" || d.Repo == "" {
			continue
		}
		h := repos[d.Repo]
		if h == nil {
			h = &habits{pinned: map[string]int{}, sources: map[string]int{}}
			repos[d.Repo] = h
		}
		pick := picks[d.SessionID]
		if d.Trigger == "pinned" {
			if pick != "" && rank(d.Chosen) > rank(pick) {
				h.pins++
				h.pinned[d.Chosen]++
				h.sources[d.Cause]++
				picks[d.SessionID] = "" // one pin per overridden pick
			}
			continue
		}
		if in := d.From; in != "" || pick != "" {
			if in == "" {
				in = pick
			}
			if rank(in) < floor && d.Signals["asks_more_thinking"] == true {
				h.think++
			}
			if rank(in) < floor && d.Signals["previous_turn_interrupted"] == true {
				h.interrupts++
			}
		}
		picks[d.SessionID] = d.Chosen
	}
	var out []Suggestion
	for _, repo := range sortedKeys(repos) {
		h := repos[repo]
		n := h.pins + h.think + h.interrupts
		if n < o.MinEvents {
			continue
		}
		tier := o.Floor
		if t := top(h.pinned); t != "" && rank(t) < floor {
			tier = t // pins to a lower tier than the default floor: that one
		}
		if o.RepoFloor != nil {
			if cur := o.RepoFloor(repo); cur != "" && rank(cur) >= rank(tier) {
				continue
			}
		}
		out = append(out, Suggestion{Repo: repo, Events: n,
			Text: fmt.Sprintf("%s: add min_tier = %q to .automodel.toml", repo, tier),
			Why:  h.why(o.Floor)})
	}
	wants := map[string]int{}
	for _, f := range o.Flagged {
		wants[f[0]+"/"+f[1]]++
	}
	for _, k := range sortedKeys(wants) {
		if wants[k] >= o.MinEvents {
			out = append(out, Suggestion{Events: wants[k],
				Text: fmt.Sprintf("review the catalog criteria for %s (refresh-model-catalog skill)", k),
				Why:  fmt.Sprintf("%d flagged decisions wanted %s", wants[k], k)})
		}
	}
	return out
}

func (h *habits) why(floor string) string {
	var parts []string
	if h.pins > 0 {
		parts = append(parts, fmt.Sprintf("%d pins above Jev's pick (%s; by %s)", h.pins, kv(h.pinned), kv(h.sources)))
	}
	if h.think > 0 {
		parts = append(parts, fmt.Sprintf("%d \"think harder\" below %s", h.think, floor))
	}
	if h.interrupts > 0 {
		parts = append(parts, fmt.Sprintf("%d interrupted turns below %s", h.interrupts, floor))
	}
	return strings.Join(parts, ", ")
}

func top(m map[string]int) string {
	best, n := "", 0
	for _, k := range sortedKeys(m) {
		if m[k] > n {
			best, n = k, m[k]
		}
	}
	return best
}

func writeSuggestions(w io.Writer, ss []Suggestion) {
	if len(ss) == 0 {
		return
	}
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Events > ss[j].Events })
	fmt.Fprintln(w, "\n## Suggestions")
	fmt.Fprintln(w)
	for _, s := range ss {
		fmt.Fprintf(w, "- %s — %s\n", s.Text, s.Why)
	}
}
