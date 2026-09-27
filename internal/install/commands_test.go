package install

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// install writes /why and /flag next to settings.json, keeps a user's own
// file of the same name, and uninstall removes only its own.
func TestSlashCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	cfgPath := filepath.Join(home, "automodel", "config.toml")
	os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	os.WriteFile(cfgPath, []byte(fmt.Sprintf("listen = %q\nstate_dir = %q\n", l.Addr().String(), filepath.Join(home, "state"))), 0o600)
	var calls []string
	o := Options{Exe: "/x/automodel", ConfigPath: cfgPath, SettingsPath: filepath.Join(home, ".claude", "settings.json"),
		UnitPath: filepath.Join(home, "unit"), Log: func(string, ...any) {}, Run: fakeRun(true, &calls), GOOS: "linux"}
	dir := filepath.Join(home, ".claude", "commands")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "flag.md"), []byte("mine"), 0o600)

	if err := Apply(o); err != nil {
		t.Fatal(err)
	}
	why, err := os.ReadFile(filepath.Join(dir, "why.md"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cmdArg(cfgPath)
	for _, want := range []string{"allowed-tools: Bash(/x/automodel --config " + cfg + " why *)",
		"!`/x/automodel --config " + cfg + ` why --session "${CLAUDE_SESSION_ID}" -n 3`} {
		if !strings.Contains(string(why), want) {
			t.Errorf("why.md lacks %q:\n%s", want, why)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "flag.md")); string(b) != "mine" {
		t.Errorf("user's flag.md overwritten: %s", b)
	}

	os.Remove(filepath.Join(dir, "flag.md"))
	installCommands(o)
	if b, _ := os.ReadFile(filepath.Join(dir, "flag.md")); !strings.Contains(string(b), `flag --session "${CLAUDE_SESSION_ID}" -- "$ARGUMENTS"`) {
		t.Errorf("flag.md:\n%s", b)
	}
	os.WriteFile(filepath.Join(dir, "other.md"), []byte("x"), 0o600)
	if err := Remove(o); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"why.md", "flag.md"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s left after uninstall", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other.md")); err != nil {
		t.Error("uninstall removed a user command")
	}
}
