// Package tokens gives a conservative token estimate for budgeting Jev state.
package tokens

import "unicode/utf8"

// Estimate over-counts on purpose (~3 chars per token) so budgets hold.
func Estimate(s string) int { return (utf8.RuneCountInString(s) + 2) / 3 }

// Truncate keeps s within max tokens, preserving the head and the tail (the
// end of a prompt often carries the actual ask).
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if Estimate(s) <= max {
		return s
	}
	r := []rune(s)
	keep := max*3 - 20
	if keep < 0 {
		keep = 0
	}
	head := keep * 2 / 3
	tail := keep - head
	return string(r[:head]) + "\n[…truncated…]\n" + string(r[len(r)-tail:])
}
