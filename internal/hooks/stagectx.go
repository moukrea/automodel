package hooks

import (
	"regexp"
	"strings"
)

// blindChars is the literal text below which a workflow stage prompt that
// reads names from its script says too little about its task to route it
// on its own words: the task comes from what it reads (live: "BASE + '\n\n' + d.p" ran
// adversarial security reviews on Sonnet low, and "${CTX}\n\n${s.angle}"
// migration plans on Sonnet).
const blindChars = 100

// maxStageContext bounds what Jev reads of the script for one stage, in
// characters (about 4k tokens).
const maxStageContext = 12000

// literalChars counts the text a stage prompt's source carries itself: the
// contents of its string and template literals, without the ${...} code.
func literalChars(src string) int {
	n := 0
	for i := 0; i < len(src); {
		switch c := src[i]; {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '\'' || c == '"':
			j := skipQuoted(src, i)
			n += max(0, j-i-2)
			i = j
		case c == '`':
			j, inner := templateText(src, i)
			n += inner
			i = j
		default:
			i++
		}
	}
	return n
}

// templateText skips a template literal from its opening backquote,
// returning the offset after it and the length of its literal text (that
// of nested templates in its ${...} code included).
func templateText(src string, i int) (int, int) {
	n := 0
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
			n++
		case '`':
			return j + 1, n
		case '$':
			if j+1 < len(src) && src[j+1] == '{' {
				end, err := skipBraces(src, j+2)
				if err != nil {
					return len(src), n
				}
				n += literalChars(src[j+2 : end])
				j = end
				continue
			}
			n++
		default:
			n++
		}
	}
	return len(src), n
}

var notRefs = map[string]bool{
	"true": true, "false": true, "null": true, "undefined": true, "new": true, "typeof": true, "await": true,
	"async": true, "return": true, "function": true, "const": true, "let": true, "var": true, "if": true,
	"else": true, "of": true, "in": true, "this": true, "JSON": true, "String": true, "Number": true,
	"Math": true, "Object": true, "Array": true, "Promise": true, "Boolean": true, "Date": true,
	"agent": true, "parallel": true, "pipeline": true, "phase": true, "log": true, "args": true,
}

// refs returns the names a stage prompt's source reads (the root of each
// member chain), in order of first use.
func refs(src string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(code string)
	walk = func(code string) {
		for i := 0; i < len(code); {
			switch c := code[i]; {
			case c == '/' && i+1 < len(code) && code[i+1] == '/':
				i = skipLineComment(code, i)
			case c == '/' && i+1 < len(code) && code[i+1] == '*':
				i = skipBlockComment(code, i)
			case c == '\'' || c == '"':
				i = skipQuoted(code, i)
			case c == '`':
				j := i + 1
				for j < len(code) && code[j] != '`' {
					switch {
					case code[j] == '\\':
						j += 2
						continue
					case code[j] == '$' && j+1 < len(code) && code[j+1] == '{':
						end, err := skipBraces(code, j+2)
						if err != nil {
							return
						}
						walk(code[j+2 : end])
						j = end
					}
					j++
				}
				i = j + 1
			case isIdentStart(c) && (i == 0 || !isIdent(code[i-1]) && code[i-1] != '.'):
				j := i
				for j < len(code) && isIdent(code[j]) {
					j++
				}
				if w := code[i:j]; !notRefs[w] && !seen[w] {
					seen[w] = true
					out = append(out, w)
				}
				i = j
			default:
				i++
			}
		}
	}
	walk(src)
	return out
}

// declaration returns the source of what a script declares under name (a
// const, let or var's initializer, or a function), "" when it declares no
// such name outside strings and comments.
func declaration(script, name string) string {
	for i := 0; i < len(script); {
		switch c := script[i]; {
		case c == '/' && i+1 < len(script) && script[i+1] == '/':
			i = skipLineComment(script, i)
		case c == '/' && i+1 < len(script) && script[i+1] == '*':
			i = skipBlockComment(script, i)
		case c == '\'' || c == '"':
			i = skipQuoted(script, i)
		case c == '`':
			j, err := skipTemplate(script, i)
			if err != nil {
				return ""
			}
			i = j
		case isIdentStart(c) && (i == 0 || !isIdent(script[i-1]) && script[i-1] != '.'):
			j := i
			for j < len(script) && isIdent(script[j]) {
				j++
			}
			kw := script[i:j]
			if kw == "const" || kw == "let" || kw == "var" || kw == "function" {
				k := skipSpace(script, j)
				e := k
				for e < len(script) && isIdent(script[e]) {
					e++
				}
				if script[k:e] == name {
					if kw == "function" {
						if b := strings.IndexByte(script[e:], '{'); b >= 0 {
							if end, err := skipBraces(script, e+b+1); err == nil {
								return strings.TrimSpace(script[i : end+1])
							}
						}
						return ""
					}
					if eq := skipSpace(script, e); eq < len(script) && script[eq] == '=' && (eq+1 >= len(script) || script[eq+1] != '=') {
						from := skipSpace(script, eq+1)
						return strings.TrimSpace(script[from:exprEnd(script, from)])
					}
				}
			}
			i = j
		default:
			i++
		}
	}
	return ""
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// exprEnd returns where an initializer started at i ends: a ';' at depth
// zero, or a line end that neither the line nor the next one continues.
func exprEnd(s string, i int) int {
	depth := 0
	for i < len(s) {
		switch c := s[i]; {
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			i = skipLineComment(s, i) - 1
			if depth == 0 && !continues(s, i) {
				return i
			}
			i++
			continue
		case c == '\'' || c == '"':
			i = skipQuoted(s, i)
			continue
		case c == '`':
			j, err := skipTemplate(s, i)
			if err != nil {
				return len(s)
			}
			i = j
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				return i
			}
			depth--
		case depth == 0 && (c == ';' || c == ','):
			return i
		case depth == 0 && c == '\n' && !continues(s, i):
			return i
		}
		i++
	}
	return len(s)
}

// continues reports a line break inside an expression: the line ends on
// an operator, or the next line starts with one.
func continues(s string, nl int) bool {
	before := strings.TrimRight(s[:nl], " \t\r")
	after := strings.TrimLeft(s[nl+1:], " \t\r\n")
	if before != "" && strings.ContainsRune("+-*/?:&|,=(<>[{.!", rune(before[len(before)-1])) {
		return true
	}
	if strings.HasPrefix(after, "//") || strings.HasPrefix(after, "/*") {
		return false // a comment line
	}
	return after != "" && strings.ContainsRune(".+-*/?:&|,)]}", rune(after[0]))
}

// paramRE finds where a name is bound as an arrow function's parameter.
func paramRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[(,\s])` + regexp.QuoteMeta(name) + `\s*(?:,[^()=]*)?\)?\s*=>`)
}

var identRE = regexp.MustCompile(`[A-Za-z_$][\w$]*`)

// iterable returns the declared array a loop parameter ranges over, for
// the stage at offset at: name bound as an arrow parameter before at, and
// the nearest name declared as an array (directly, or derived from one)
// just before that binding ("DIMS.map(d =>", "pipeline(DESIGNS, d =>").
func iterable(script string, at int, name string) string {
	locs := paramRE(name).FindAllStringIndex(script[:min(at, len(script))], -1)
	if len(locs) == 0 {
		return ""
	}
	p := locs[len(locs)-1][0]
	window := script[max(0, p-300):p]
	ids := identRE.FindAllString(window, -1)
	for k := len(ids) - 1; k >= 0; k-- {
		if arr := arrayNamed(script, ids[k], 2); arr != "" {
			return arr
		}
	}
	return ""
}

// arrayNamed returns name when the script declares it as an array literal,
// or the array it is derived from ("const first = MODULES.filter(...)"),
// following up to depth declarations.
func arrayNamed(script, name string, depth int) string {
	if notRefs[name] {
		return ""
	}
	d := declaration(script, name)
	switch {
	case d == "":
		return ""
	case strings.HasPrefix(d, "["):
		return name
	case depth == 0:
		return ""
	}
	for _, id := range identRE.FindAllString(d, -1) {
		if id != name {
			if arr := arrayNamed(script, id, depth-1); arr != "" {
				return arr
			}
		}
	}
	return ""
}

// stageContext is what a stage prompt that says little itself reads from
// its script: the array its loop parameter ranges over (one agent per
// element), then the functions and constants it uses, each within its
// share of maxChars. Empty when nothing is found.
func stageContext(script string, s CallSite, maxChars int) string {
	if len(s.Args) == 0 {
		return ""
	}
	var loops, others []string
	seen := map[string]bool{}
	for _, r := range refs(s.Args[0]) {
		if d := declaration(script, r); d != "" {
			if !seen[r] {
				seen[r] = true
				others = append(others, r+" = "+d)
			}
			continue
		}
		if arr := iterable(script, s.Start, r); arr != "" && !seen[arr] {
			seen[arr] = true
			loops = append(loops, "// "+r+" ranges over "+arr+": one agent per element\n"+arr+" = "+declaration(script, arr))
		}
	}
	if len(loops)+len(others) == 0 {
		return ""
	}
	// The task is in the elements a loop ranges over, else in the last name
	// the prompt reads; what comes before it is shared context (a project
	// preamble, the rules), kept short: read in full it makes every stage
	// look as big as the whole project (live: map and fact-finding stages
	// read at max under a migration preamble).
	var task, shared []string
	if len(loops) > 0 {
		task, shared = loops, others
	} else {
		task, shared = others[len(others)-1:], others[:len(others)-1]
	}
	clip(shared, min(len(shared)*sharedChars, maxChars/3))
	clip(task, maxChars-joinedLen(shared))
	return strings.Join(append(shared, task...), "\n\n")
}

// sharedChars is the room for each piece of shared context.
const sharedChars = 500

// clip cuts each part to its share of room.
func clip(parts []string, room int) {
	if len(parts) == 0 {
		return
	}
	share := max(room/len(parts), 200)
	for i, p := range parts {
		if len(p) > share {
			parts[i] = strings.ToValidUTF8(p[:share], "") + "…"
		}
	}
}

func joinedLen(parts []string) int {
	n := 0
	for _, p := range parts {
		n += len(p) + 2
	}
	return n
}
