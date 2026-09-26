package install

import (
	"encoding/json"
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
