package statusline

import "testing"

// The chained command runs in Git Bash when there is one, else PowerShell.
var chainedEnvEcho = func() string {
	if gitBash() != "" {
		return "echo chained=$AUTOMODEL_CHAINED"
	}
	return "Write-Output \"chained=$env:AUTOMODEL_CHAINED\""
}()

// Without Git Bash the chained statusline runs in PowerShell, with the
// statusline JSON on its stdin.
func TestChainPowerShell(t *testing.T) {
	noGitBash = true
	t.Cleanup(func() { noGitBash = false })
	dir := t.TempDir()
	var got string
	for i := 0; i < 10 && got == ""; i++ { // PowerShell may start slower than a first run waits
		got = chain(dir, "[Console]::In.ReadToEnd()", "ps1", []byte(`{"a":1}`))
	}
	if got != `{"a":1}` {
		t.Fatalf("chain = %q", got)
	}
}

// With Git Bash (the CI runners have it), the paths reach bash intact.
func TestChainGitBash(t *testing.T) {
	if gitBash() == "" {
		t.Skip("no Git Bash")
	}
	got := chain(t.TempDir(), "cat; echo; echo mine", "gb", []byte(`{"a":1}`))
	if got != "{\"a\":1}\nmine" {
		t.Fatalf("chain = %q", got)
	}
}
