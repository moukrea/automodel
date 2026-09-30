package router

import "testing"

// Proposes: a question or an offer at the end of the assistant's message,
// also when a remark follows it (live: "Should I push the branch? Pushing
// it would also push the README typo fix (cab8abd)." took the bare
// go-ahead path and resumed the paused work), or a list and a remark, or a
// remark of a few sentences.
func TestProposes(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"Want me to fix it?", true},
		{"Should I also add a test?\n", true},
		{"**Want me to open the PR?**", true},
		{"Committed.\n\nAnything else?", true},
		{"Should I push the branch? Pushing it would also push the README typo fix (cab8abd).", true},
		{"Committed.\n\nShould I push the branch?\n\nIt would also push the README typo fix.", true},
		{"C'est commité. Dois-je pousser ?", true},
		{"Le fix est prêt, dis-moi si j'applique le fix sur les deux autres fichiers.", true},
		{"The same typo is in the man page, if you want it fixed there too.", true},
		{"Let me know if you want the index added as well.", true},
		{"Fixed. The same sleep is in three more specs, want me to fix them too?\n\n- `spec/a_spec.rb:42`\n- `spec/b_spec.rb:17`\n\nThey are all the same one-line change.", true},
		{"Committed. Shall I push it?\n\n" + long, true},
		{"Committed. I can fix the two other typos in docs/ too if needed.", true},
		{"C'est commité. Tu me dis si je pousse.", true},
		{"C'est commité. Si besoin, je pousse la branche.", true},
		{"C'est commité. Je corrige aussi les deux autres fautes si tu le veux.", true},
		{"C'est commité. Je pousse ?\u00a0Sinon on reprend la migration.", true},
		{"C'est commité. Je pousse ?\u202fSinon on reprend la migration.", true},
		{"Done, all tests pass.", false},
		{"Fixed. The handler now reads `r.URL.Query().Get(\"page\")` for `?page=2`.", false},
		{"Done.\n\n```go\nx := ok ? a : b\n```", false},
		{"See https://example.com/search?q=retry for the docs.", false},
		// A question far above the end is not on the table.
		{"Why did it fail? The lease was read without the lock.\n\n" + long + "\n\n" + long, false},
		{"Should I push it?\n\nDone.\n\n- a\n- b\n\nAll green.", false},
		{"", false},
	} {
		if got := Proposes(tc.text); got != tc.want {
			t.Errorf("Proposes(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

const long = "The fix takes the lock before reading the lease and releases it once the connection is handed over; the pool's tests now run the checkout path under the race detector with 50 workers, and the benchmark shows no measurable change in throughput on the hot path."
