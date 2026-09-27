package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/moukrea/automodel/internal/ledger"
)

// FromDecision turns a decision the user flagged into a labeled case: the
// routing state it sent, the tier the user wanted, and a note with the
// reason and Jev's probabilities.
func FromDecision(d ledger.Decision, st *ledger.StateRecord, want, reason string) Case {
	c := Case{ID: "flag-" + d.ID, Scope: d.Scope, Warm: st.Warm, State: st.State, Want: want, Accept: []string{want}}
	var parts []string
	if reason != "" {
		parts = append(parts, reason)
	}
	parts = append(parts, fmt.Sprintf("picked %s (Jev %s, confidence %.2f)", d.Chosen, d.JevChoice, d.Confidence))
	if p := probs(d.Probs); p != "" {
		parts = append(parts, "probs "+p)
	}
	c.Note = strings.Join(parts, "; ")
	return c
}

func probs(m map[string]float64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	var out []string
	for _, k := range keys {
		out = append(out, fmt.Sprintf("%s=%.2f", k, m[k]))
	}
	return strings.Join(out, " ")
}

// AppendCase appends one case to a JSONL file (0600).
func AppendCase(path string, c Case) error {
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
