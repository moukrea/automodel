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
// the relation question). Its end is the last paragraph, with the ones
// before it while only remarks or lists follow (closing); code is left
// out.
func Proposes(lastAssistant string) bool {
	tail := closing(lastAssistant)
	return questionRE.MatchString(tail) || offerRE.MatchString(tail)
}

var (
	// A question mark that ends a sentence (not "?page=2" nor "a ? b : c"
	// in code, which closing drops), also before a no-break space.
	questionRE = regexp.MustCompile(`\?+(?:[\s\x{00A0}\x{202F}*_)"'»”’]|$)`)
	// An offer in words, without a question mark.
	offerRE      = regexp.MustCompile(`(?i)(?:^|[^\pL])(?:let\s+me\s+know\s+(?:if|whether|when)|if\s+you(?:['’]d|\s+would)?\s+(?:like|want|prefer)|if\s+needed|say\s+the\s+word|just\s+say\s+so|(?:dis|dites)-moi\s+(?:si|quand)|(?:tu\s+me\s+dis|vous\s+me\s+dites)\s+(?:si|quand)|si\s+besoin|si\s+(?:tu|vous)\s+(?:le\s+)?(?:veux|voulez|souhaites|souhaitez|préfères|préférez))(?:[^\pL]|$)`)
	listRE       = regexp.MustCompile(`^\s*(?:[-*+•|]|\d+[.)])`)
	codeBlockRE  = regexp.MustCompile("(?s)```.*?(?:```|$)")
	inlineCodeRE = regexp.MustCompile("`[^`\n]*`")
	paragraphRE  = regexp.MustCompile(`\n\s*\n`)
)

// closing is the end of an assistant message: its last paragraph, with
// the ones before it while what follows them is only a remark or lists (at
// most two paragraphs: "Want me to fix them too?", then the files it
// names, then "They are all the same one-line change.").
func closing(text string) string {
	text = inlineCodeRE.ReplaceAllString(codeBlockRE.ReplaceAllString(text, " "), " ")
	var ps []string
	for _, p := range paragraphRE.Split(text, -1) {
		if p = strings.TrimSpace(p); p != "" {
			ps = append(ps, p)
		}
	}
	i, remark := len(ps)-1, 0
	for ; i > 0 && len(ps)-i <= 2; i-- {
		if !list(ps[i]) {
			if remark += len([]rune(ps[i])); remark > remarkRunes {
				break
			}
		}
	}
	return strings.Join(ps[max(i, 0):], "\n\n")
}

// remarkRunes is the longest remark that may follow a question: two or
// three sentences ("Should I push? CI takes about ten minutes, and ...").
const remarkRunes = 300

// list reports a paragraph that is only a list or a table.
func list(p string) bool {
	for _, l := range strings.Split(p, "\n") {
		if !listRE.MatchString(l) {
			return false
		}
	}
	return true
}
