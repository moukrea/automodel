package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// With $AUTOMODEL_TEST_RUN set, the test binary just runs for a while: a
// stand-in for the running proxy whose binary gets replaced.
func TestMain(m *testing.M) {
	if os.Getenv("AUTOMODEL_TEST_RUN") != "" {
		time.Sleep(30 * time.Second)
		return
	}
	os.Exit(m.Run())
}

// Windows can't overwrite a running executable: Apply moves it aside.
func TestApplyOverRunningBinary(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "automodel.exe")
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := os.Create(exe)
	io.Copy(dst, src)
	src.Close()
	dst.Close()
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "AUTOMODEL_TEST_RUN=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	arch := archive(t, []byte("new"))
	sum := sha256.Sum256(arch)
	name := ArchiveName("v1.2.3")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Write(arch)
		case "/sums":
			w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + name + "\n"))
		}
	}))
	defer srv.Close()
	rel := &Release{Tag: "v1.2.3", Assets: []Asset{{Name: name, URL: srv.URL + "/a"}, {Name: "checksums.txt", URL: srv.URL + "/sums"}}}
	if err := Apply(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new" {
		t.Fatalf("binary = %.20q", b)
	}
	if _, err := os.Stat(exe + ".old"); err != nil {
		t.Fatalf("old binary: %v", err)
	}
	RemoveOld(exe) // still running: kept
	cmd.Process.Kill()
	cmd.Wait()
	for i := 0; i < 50; i++ {
		RemoveOld(exe)
		if _, err := os.Stat(exe + ".old"); os.IsNotExist(err) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("old binary not removed once stopped")
}
