package router

import (
	"regexp"
	"strings"
)

// goAheads are prompts that only tell Claude to carry on: a go-ahead,
// optionally after an agreement and before a "please" ("oui, vas-y",
// "ok, go", "yes, do it", "parfait, fonce", "continue then").
var goAheads = map[string]bool{}

func init() {
	agree := []string{"", "y", "yes", "yep", "yeah", "yup", "ok", "okay", "k", "sure", "alright", "right", "please",
		"cool", "great", "perfect", "sounds good", "looks good", "lgtm", "👍",
		"oui", "ouais", "d'accord", "dac", "parfait", "super", "nickel", "top", "bon", "très bien", "c'est bon", "ok cool"}
	goes := []string{"", "go", "go ahead", "go on", "go for it", "go go", "go go go", "let's go", "let's do it", "do it", "please do",
		"continue", "carry on", "keep going", "proceed", "ship it", "you can go ahead",
		"vas-y", "allez", "allez-y", "on y va", "fonce", "c'est parti", "poursuis", "fais-le", "on continue", "tu peux y aller"}
	please := []string{"", "please", "stp", "svp", "then", "now", "alors", "maintenant"}
	for _, a := range agree {
		for _, g := range goes {
			for _, p := range please {
				if w := normGoAhead(a + " " + g + " " + p); w != "" {
					goAheads[w] = true
				}
			}
		}
	}
}

// goAheadPunct is punctuation a go-ahead may carry anywhere ("oui, vas-y !"):
// dropped before the lookup. A question mark is not: "continue?" asks.
var goAheadPunct = regexp.MustCompile(`[.!,;:…()"«»*_~\-–—]+`)

func normGoAhead(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "’", "'")
	return strings.Join(strings.Fields(goAheadPunct.ReplaceAllString(s, " ")), " ")
}

// GoAhead reports whether a prompt is only a go-ahead.
func GoAhead(prompt string) bool {
	if len(prompt) > 64 {
		return false
	}
	return goAheads[normGoAhead(prompt)]
}

// Proposes reports whether the assistant's last message ends on a question
// ("Want me to fix it?"): a go-ahead then starts the proposed work, which
// can be bigger than the work so far, so it is routed rather than carried.
func Proposes(lastAssistant string) bool {
	return strings.HasSuffix(strings.TrimRight(lastAssistant, " \t\n*_`)"), "?")
}
