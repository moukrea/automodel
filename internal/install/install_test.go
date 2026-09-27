package install

import (
	"encoding/json"
	"os"
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
