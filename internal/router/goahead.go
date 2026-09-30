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

// Proposes reports whether the assistant's last message puts a question or
// an offer on the table at its end ("Want me to fix it?", "Should I push
// the branch? It would also push the typo fix.", "dis-moi si j'applique le
// fix"): a go-ahead then answers it, and may start work bigger than the
// work so far, so it is routed rather than carried (after a detour, with
// the relation question). Its end is the last paragraph, and the one
// before when the last is a short remark; code is left out.
func Proposes(lastAssistant string) bool {
	tail := closing(lastAssistant)
	return questionRE.MatchString(tail) || offerRE.MatchString(tail)
}

var (
	// A question mark that ends a sentence (not "?page=2" nor "a ? b : c"
	// in code, which closing drops).
	questionRE = regexp.MustCompile(`\?+(?:[\s*_)"'»”’]|$)`)
	// An offer in words, without a question mark.
	offerRE      = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:let\s+me\s+know\s+(?:if|whether|when)|if\s+you(?:['’]d|\s+would)?\s+(?:like|want|prefer)|say\s+the\s+word|just\s+say\s+so|(?:dis|dites)-moi\s+(?:si|quand)|si\s+(?:tu|vous)\s+(?:veux|voulez|le\s+souhaites|le\s+souhaitez|préfères|préférez))(?:[^\pL]|$)`)
	codeBlockRE  = regexp.MustCompile("(?s)```.*?(?:```|$)")
	inlineCodeRE = regexp.MustCompile("`[^`\n]*`")
	paragraphRE  = regexp.MustCompile(`\n\s*\n`)
)

// closing is the end of an assistant message: its last paragraph, with
// the one before when the last is short (a remark after the question).
func closing(text string) string {
	text = inlineCodeRE.ReplaceAllString(codeBlockRE.ReplaceAllString(text, " "), " ")
	var ps []string
	for _, p := range paragraphRE.Split(text, -1) {
		if p = strings.TrimSpace(p); p != "" {
			ps = append(ps, p)
		}
	}
	switch n := len(ps); {
	case n == 0:
		return ""
	case n > 1 && len([]rune(ps[n-1])) <= 200:
		return ps[n-2] + "\n\n" + ps[n-1]
	default:
		return ps[n-1]
	}
}
