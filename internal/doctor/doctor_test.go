package doctor

import (
	"runtime"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moukrea/automodel"
	"github.com/moukrea/automodel/internal/config"
	"github.com/moukrea/automodel/internal/install"
	"github.com/moukrea/automodel/internal/proxy"
)

// setup builds a healthy install in a temp dir: config, shipped catalog,
// settings.json from install.Preview, a fake proxy and a fake OpenRouter.
func setup(t *testing.T) (Env, *httptest.Server) {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "")
	dir := t.TempDir()
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == proxy.HealthPath {
			fmt.Fprint(w, `{"version":"v1.0.0","pid":1}`)
		}
	}))
	t.Cleanup(px.Close)
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-or-good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"data":{"label":"x","limit":null,"limit_remaining":null,"usage":0.1}}`)
	}))
	t.Cleanup(or.Close)
	os.WriteFile(filepath.Join(dir, "catalog.toml"), automodel.Catalog, 0o600)
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(fmt.Sprintf("catalog = \"catalog.toml\"\nstate_dir = \"state\"\nlisten = %q\nopenrouter_api_key = \"sk-or-good\"\n",
		strings.TrimPrefix(px.URL, "http://"))), 0o600)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	o := install.Options{Exe: "/x/automodel", ConfigPath: cfgPath, SettingsPath: filepath.Join(dir, "settings.json"),
		UnitPath: filepath.Join(dir, "automodel.service"), GOOS: "linux",
		Run: func(string, ...string) ([]byte, error) { return []byte("active\n"), nil }}
	os.WriteFile(o.UnitPath, nil, 0o600)
	b, _ := install.Preview(o, cfg)
	os.WriteFile(o.SettingsPath, b, 0o600)
	e := Env{Version: "v1.0.0", UpdateCmd: "automodel update", Cfg: cfg, Install: o, KeyURL: or.URL,
		Latest: func(context.Context) (string, error) { return "v1.0.0", nil }}
	return e, px
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q check", name)
	return Result{}
}

func TestHealthyInstall(t *testing.T) {
	e, _ := setup(t)
	rs := Run(e)
	var out bytes.Buffer
	if n := Print(&out, rs); n != 0 {
		t.Errorf("%d failures:\n%s", n, &out)
	}
	for _, r := range rs {
		if r.Status != OK {
			t.Errorf("not ok: %s %s %s", r.Status, r.Name, r.Detail)
		}
	}
	if strings.Contains(out.String(), "sk-or-good") {
		t.Error("the key was printed")
	}
}

func TestProblemsAreReported(t *testing.T) {
	e, px := setup(t)
	e.Cfg.OpenRouterAPIKey = "sk-or-bad"
	e.Latest = func(context.Context) (string, error) { return "v1.1.0", nil }
	s, _ := os.ReadFile(e.Install.SettingsPath)
	os.WriteFile(e.Install.SettingsPath, bytes.ReplaceAll(s, []byte(" hook decide"), []byte(" hook nope")), 0o600)
	px.Close()
	rs := Run(e)
	for name, want := range map[string]Status{"openrouter": Fail, "binary": Warn, "proxy": Fail, "hooks": Fail, "base URL": OK} {
		if r := find(t, rs, name); r.Status != want || (want != OK && r.Fix == "") {
			t.Errorf("%s: %s %q fix %q, want %s", name, r.Status, r.Detail, r.Fix, want)
		}
	}
	if r := find(t, rs, "hooks"); !strings.Contains(r.Detail, "decide") {
		t.Errorf("hooks: %s", r.Detail)
	}
}

func TestNoKeySkipsOpenRouter(t *testing.T) {
	e, _ := setup(t)
	e.Cfg.OpenRouterAPIKey = ""
	rs := Run(e)
	if r := find(t, rs, "openrouter"); r.Status != Warn {
		t.Errorf("openrouter without key: %s %s", r.Status, r.Detail)
	}
	if r := find(t, rs, "key"); r.Status != Warn || r.Detail == "" {
		t.Errorf("key: %s %s", r.Status, r.Detail)
	}
}

func TestBaseURLElsewhere(t *testing.T) {
	e, _ := setup(t)
	s, _ := os.ReadFile(e.Install.SettingsPath)
	os.WriteFile(e.Install.SettingsPath, bytes.Replace(s, []byte("http://127.0.0.1"), []byte("http://10.0.0.1"), 1), 0o600)
	if r := find(t, Run(e), "base URL"); r.Status != Fail {
		t.Errorf("base URL: %s %s", r.Status, r.Detail)
	}
}

func TestLedgerNotWritable(t *testing.T) {
	e, _ := setup(t)
	ro := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(ro, 0o500)
	e.Cfg.Ledger = filepath.Join(ro, "ledger.jsonl")
	if os.Geteuid() == 0 || runtime.GOOS == "windows" {
		t.Skip("root writes anywhere; Windows ignores directory permission bits")
	}
	if r := find(t, Run(e), "ledger"); r.Status != Fail {
		t.Errorf("ledger: %s %s", r.Status, r.Detail)
	}
}
