package hooks

import (
	"errors"
	"strings"
)

// CallSite is one agent(...) call found in a workflow script.
type CallSite struct {
	Start, End int      // byte offsets of the argument list: script[Start:End] excludes the parens
	Args       []string // top-level argument sources, trimmed
}

var errUnbalanced = errors.New("unbalanced script")

// findAgentCalls lexes a workflow script just enough to find agent(...) call
// sites outside strings, template literals and comments, and to split their
// top-level arguments. Regex literals are not recognised: an unbalanced scan
// returns an error and the script is left alone.
func findAgentCalls(src string) ([]CallSite, error) {
	var sites []CallSite
	i := 0
	for i < len(src) {
		switch c := src[i]; {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
		case c == '\'' || c == '"':
			i = skipQuoted(src, i)
		case c == '`':
			j, err := skipTemplate(src, i)
			if err != nil {
				return nil, err
			}
			i = j
		case isIdentStart(c) && (i == 0 || !isIdent(src[i-1]) && src[i-1] != '.'):
			j := i
			for j < len(src) && isIdent(src[j]) {
				j++
			}
			word := src[i:j]
			k := j
			for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
				k++
			}
			if word == "agent" && k < len(src) && src[k] == '(' && !precededByDecl(src, i) {
				end, args, err := splitArgs(src, k+1)
				if err != nil {
					return nil, err
				}
				sites = append(sites, CallSite{Start: k + 1, End: end, Args: args})
				i = end + 1
				continue
			}
			i = j
		default:
			i++
		}
	}
	return sites, nil
}

// splitArgs scans from just after '(' to the matching ')', returning its
// offset and the top-level arguments.
func splitArgs(src string, from int) (int, []string, error) {
	depth := 0
	argStart := from
	var args []string
	for i := from; i < len(src); {
		switch c := src[i]; {
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			i = skipLineComment(src, i)
			continue
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i = skipBlockComment(src, i)
			continue
		case c == '\'' || c == '"':
			i = skipQuoted(src, i)
			continue
		case c == '`':
			j, err := skipTemplate(src, i)
			if err != nil {
				return 0, nil, err
			}
			i = j
			continue
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			if depth == 0 {
				if c != ')' {
					return 0, nil, errUnbalanced
				}
				if a := strings.TrimSpace(src[argStart:i]); a != "" {
					args = append(args, a)
				}
				return i, args, nil
			}
			depth--
		case c == ',' && depth == 0:
			args = append(args, strings.TrimSpace(src[argStart:i]))
			argStart = i + 1
		}
		i++
	}
	return 0, nil, errUnbalanced
}

func skipLineComment(src string, i int) int {
	if j := strings.IndexByte(src[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(src)
}

func skipBlockComment(src string, i int) int {
	if j := strings.Index(src[i+2:], "*/"); j >= 0 {
		return i + 2 + j + 2
	}
	return len(src)
}

func skipQuoted(src string, i int) int {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case q, '\n':
			return j + 1
		}
	}
	return len(src)
}

// skipTemplate skips a template literal, including nested ${...} code.
func skipTemplate(src string, i int) (int, error) {
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '`':
			return j + 1, nil
		case '$':
			if j+1 < len(src) && src[j+1] == '{' {
				end, err := skipBraces(src, j+2)
				if err != nil {
					return 0, err
				}
				j = end
			}
		}
	}
	return 0, errUnbalanced
}

// skipBraces returns the offset of the '}' closing an expression started at i.
func skipBraces(src string, i int) (int, error) {
	depth := 0
	for i < len(src) {
		switch c := src[i]; c {
		case '\'', '"':
			i = skipQuoted(src, i)
			continue
		case '`':
			j, err := skipTemplate(src, i)
			if err != nil {
				return 0, err
			}
			i = j
			continue
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i, nil
			}
			depth--
		}
		i++
	}
	return 0, errUnbalanced
}

func precededByDecl(src string, i int) bool {
	before := strings.TrimRight(src[:i], " \t")
	return strings.HasSuffix(before, "function") || strings.HasSuffix(before, "async")
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' }

// hasKey reports whether an object literal source sets key at top level.
func hasKey(obj, key string) bool {
	if !strings.HasPrefix(obj, "{") || !strings.HasSuffix(obj, "}") {
		return false
	}
	_, args, err := splitArgs(obj[1:len(obj)-1]+")", 0)
	if err != nil {
		return false
	}
	for _, a := range args {
		k := strings.TrimSpace(strings.SplitN(a, ":", 2)[0])
		k = strings.Trim(k, `'"`)
		if k == key {
			return true
		}
	}
	return false
}
