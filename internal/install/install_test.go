package install

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/moukrea/automodel/internal/config"
)

const sample = `{
  "env": {"TMP": "/t"},
  "permissions": {"allow": ["Read"], "defaultMode": "auto"},
  "model": "opus",
  "hooks": {
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "/x/hook-claude", "timeout": 10}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "/x/hook-claude"}]}]
  },
  "statusLine": {"type": "command", "command": "bash agentline.sh", "refreshInterval": 1},
  "effortLevel": "xhigh"
}`

func TestMergeRoundTrip(t *testing.T) {
	src := sample
	s, err := ParseObject([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := json.Marshal(s)
	cfg := config.Default()
	o := Options{Exe: "/home/u/.local/bin/automodel", ConfigPath: "/home/u/.config/automodel/config.toml"}
	merge(s, o, cfg)
	merge(s, o, cfg) // idempotent
	b, _ := json.Marshal(s)
	got := string(b)
	for _, want := range []string{`"ANTHROPIC_BASE_URL":"http://127.0.0.1:8788"`, `"Workflow"`, `hook decide`, `"refreshInterval":1`, `"/x/hook-claude"`, `"matcher":"Agent|Task"`, `"_CLAUDE_CODE_ASSUME_FIRST_PARTY_BASE_URL":"1"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if n := strings.Count(got, "hook decide"); n != 1 {
		t.Errorf("decide hook %d times", n)
	}
	if !strings.HasPrefix(got, `{"env":`) {
		t.Errorf("key order lost: %.60s", got)
	}
	// Undo: same removals as Remove.
	env := s.Obj("env")
	for _, kv := range envVars(cfg) {
		env.Delete(kv[0])
	}
	removeOwnedHooks(s.Obj("hooks"))
	sl := s.Obj("statusLine")
	sl.Set("command", "bash agentline.sh")
	perms := s.Obj("permissions")
	a, _ := perms.Get("allow")
	l := a.([]any)
	perms.Set("allow", l[:len(l)-1])
	back, _ := json.Marshal(s)
	if string(back) != string(orig) {
		t.Errorf("round trip:\n%s\n%s", orig, back)
	}
}

// Only an api.anthropic.com upstream may be declared first-party; switching
// upstream drops a stale declaration.
func TestFirstPartyOnlyForAnthropicUpstream(t *testing.T) {
	s := NewObject()
	cfg := config.Default()
	o := Options{Exe: "/x/automodel", ConfigPath: "/x/config.toml"}
	merge(s, o, cfg)
	if v, _ := s.Obj("env").Get(firstPartyEnv); v != "1" {
		t.Fatalf("default upstream: %s = %v", firstPartyEnv, v)
	}
	cfg.Upstream = "https://gateway.example.com"
	merge(s, o, cfg)
	if _, ok := s.Obj("env").Get(firstPartyEnv); ok {
		t.Errorf("%s kept for a third-party upstream", firstPartyEnv)
	}
}

func TestRefreshLeavesOtherInstallsAlone(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/settings.json"
	cfg := config.Default()
	mine := Options{Exe: "/u/bin/automodel", ConfigPath: "/u/.config/automodel/config.toml", SettingsPath: path, Log: func(string, ...any) {}}
	s, _ := ParseObject([]byte(sample))
	merge(s, mine, cfg)
	b, _ := json.Marshal(s)
	os.WriteFile(path, b, 0o600)

	// A second instance (another config) must not retarget the settings.
	other := mine
	other.ConfigPath = "/tmp/test/config.toml"
	cfg2 := config.Default()
	cfg2.Listen = "127.0.0.1:8798"
	if err := Refresh(other, cfg2); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(b) || strings.Contains(string(got), "8798") {
		t.Fatalf("another install rewrote the settings:\n%s", got)
	}
	// Settings nobody installed are left alone too.
	os.WriteFile(path, []byte(sample), 0o600)
	if err := Refresh(mine, cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != sample {
		t.Fatal("refresh installed automodel into settings it didn't own")
	}
	// The owning install still refreshes its own entries.
	s, _ = ParseObject([]byte(sample))
	merge(s, mine, cfg)
	s.Obj("env").Delete("CLAUDE_CODE_MAX_CONTEXT_TOKENS")
	b, _ = json.Marshal(s)
	os.WriteFile(path, b, 0o600)
	if err := Refresh(mine, cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !strings.Contains(string(got), "CLAUDE_CODE_MAX_CONTEXT_TOKENS") {
		t.Fatal("the owning install didn't refresh its entries")
	}
}

// Paths with spaces are quoted for the shell, and the entries are still
// recognized as this install's.
func TestCommandsQuotePaths(t *testing.T) {
	path := t.TempDir() + "/settings.json"
	cfg := config.Default()
	o := Options{Exe: "/home/J Doe/bin/automodel", ConfigPath: "/home/J Doe/.config/automodel/config.toml", SettingsPath: path, Log: func(string, ...any) {}}
	if got := o.hookCmd("decide"); got != "'/home/J Doe/bin/automodel' --config '/home/J Doe/.config/automodel/config.toml' hook decide" && runtime.GOOS != "windows" {
		t.Errorf("hook command %q", got)
	}
	if !owned(o.hookCmd("decide")) || !owned(o.statuslineCmd()) {
		t.Error("quoted commands not recognized as automodel's")
	}
	s := NewObject()
	merge(s, o, cfg)
	if !ownedBy(s, o) {
		t.Error("quoted settings not recognized as this install's")
	}
	if cmdArg("/home/u/.local/bin/automodel") != "/home/u/.local/bin/automodel" && runtime.GOOS != "windows" {
		t.Error("plain path changed")
	}
}

// automodel's hooks are updated where they stand: other tools' hooks placed
// after them stay after them, and a merge over up-to-date settings changes
// nothing (no rewrite, no backup). A stale duplicate and an old binary path
// are fixed in place.
func TestMergeKeepsHookOrder(t *testing.T) {
	o := Options{Exe: "/home/u/.local/bin/automodel", ConfigPath: "/home/u/.config/automodel/config.toml"}
	cfg := config.Default()
	s := NewObject()
	merge(s, o, cfg)
	hooks := s.Obj("hooks")
	for _, ev := range []string{"UserPromptSubmit", "SessionStart"} {
		groups, _ := hooks.Get(ev)
		g := NewObject()
		h := NewObject()
		h.Set("type", "command")
		h.Set("command", "/x/hook-claude")
		g.Set("hooks", []any{h})
		hooks.Set(ev, append(groups.([]any), g))
	}
	raw, _ := json.Marshal(s)
	s, _ = ParseObject(raw)
	before, _ := json.Marshal(s)
	merge(s, o, cfg)
	after, _ := json.Marshal(s)
	if string(before) != string(after) {
		t.Fatalf("merge over current settings changed them:\n%s\n%s", before, after)
	}

	// An old binary path and a stale duplicate: updated in place, duplicate dropped.
	old := strings.ReplaceAll(string(before), "/home/u/.local/bin/automodel", "/old/automodel")
	s, _ = ParseObject([]byte(old))
	groups, _ := s.Obj("hooks").Get("UserPromptSubmit")
	dup := NewObject()
	dh := NewObject()
	dh.Set("type", "command")
	dh.Set("command", "/older/automodel --config /c hook decide")
	dup.Set("hooks", []any{dh})
	s.Obj("hooks").Set("UserPromptSubmit", append(groups.([]any), dup))
	merge(s, o, cfg)
	got, _ := json.Marshal(s)
	if string(got) != string(before) {
		t.Errorf("stale hooks not fixed in place:\n%s\n%s", before, got)
	}
}

func TestRefreshLeavesCurrentSettings(t *testing.T) {
	dir := t.TempDir()
	o := Options{Exe: "/home/u/.local/bin/automodel", ConfigPath: "/home/u/.config/automodel/config.toml", SettingsPath: dir + "/settings.json", Log: func(string, ...any) {}}
	cfg := config.Default()
	s := NewObject()
	merge(s, o, cfg)
	g := NewObject()
	h := NewObject()
	h.Set("type", "command")
	h.Set("command", "/x/hook-claude")
	g.Set("hooks", []any{h})
	groups, _ := s.Obj("hooks").Get("UserPromptSubmit")
	s.Obj("hooks").Set("UserPromptSubmit", append(groups.([]any), g))
	if err := writeSettings(o.SettingsPath, s); err != nil {
		t.Fatal(err)
	}
	if err := Refresh(o, cfg); err != nil {
		t.Fatal(err)
	}
	m, _ := os.ReadDir(dir)
	for _, e := range m {
		if strings.Contains(e.Name(), "automodel-backup") {
			t.Errorf("refresh wrote a backup: %s", e.Name())
		}
	}
}
