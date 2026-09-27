package router

import (
	"regexp"
	"strings"
)

// goAheads are prompts that only tell Claude to carry on.
var goAheads = map[string]bool{}

func init() {
	for _, p := range []string{"y", "yes", "yep", "yeah", "yup", "ok", "okay", "k", "sure", "go", "go ahead", "go on",
		"continue", "carry on", "keep going", "proceed", "do it", "lgtm", "sounds good", "looks good", "perfect", "great",
		"oui", "ouais", "ok go", "vas y", "vas-y", "go go", "continue stp", "continue please", "please continue", "yes please",
		"d'accord", "dac", "parfait", "fonce", "allez", "allez-y", "c'est bon", "c'est parti", "on y va", "ok vas-y", "oui vas-y"} {
		goAheads[p] = true
	}
}

var goAheadTrim = regexp.MustCompile(`[\s.!,;:]+$`)

// GoAhead reports whether a prompt is only a go-ahead.
func GoAhead(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = goAheadTrim.ReplaceAllString(p, "")
	p = strings.Join(strings.Fields(p), " ")
	return len(p) <= 24 && goAheads[p]
}

// Proposes reports whether the assistant's last message ends on a question
// ("Want me to fix it?"): a go-ahead then starts the proposed work, which
// can be bigger than the work so far, so it is routed rather than carried.
func Proposes(lastAssistant string) bool {
	return strings.HasSuffix(strings.TrimRight(lastAssistant, " \t\n*_`)"), "?")
}
