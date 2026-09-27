package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		cur, tag string
		want     bool
	}{
		{"0.1.0", "v0.1.1", true}, {"v0.2.0", "v0.1.9", false}, {"0.1.0", "v0.1.0", false},
		{"1.2.3", "v2.0.0", true}, {"dev", "v9.9.9", false}, {"0.1.0", "garbage", false},
	} {
		if got := Newer(c.cur, c.tag); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.cur, c.tag, got)
		}
	}
}

// archive builds the release archive for this platform.
func archive(t *testing.T, content []byte) []byte {
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("automodel.exe")
		w.Write(content)
		zw.Close()
		return buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "automodel", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestApply(t *testing.T) {
	arch := archive(t, []byte("#!/bin/sh\necho new\n"))
	sum := sha256.Sum256(arch)
	name := ArchiveName("v1.2.3")
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + Repo + "/releases/latest":
			json.NewEncoder(w).Encode(Release{Tag: "v1.2.3", Assets: []Asset{
				{Name: name, URL: srv.URL + "/a"}, {Name: "checksums.txt", URL: srv.URL + "/sums"}}})
		case "/a":
			w.Write(arch)
		case "/sums":
			w.Write([]byte(sums))
		}
	}))
	defer srv.Close()
	API = srv.URL
	rel, err := Latest(context.Background())
	if err != nil || rel.Tag != "v1.2.3" {
		t.Fatal(rel, err)
	}
	exe := filepath.Join(t.TempDir(), "automodel")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Apply(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "#!/bin/sh\necho new\n" {
		t.Errorf("binary = %q", b)
	}
	// A tampered archive is refused.
	arch = archive(t, []byte("evil"))
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := Apply(context.Background(), rel, exe); err == nil {
		t.Error("tampered archive accepted")
	}
	if b, _ := os.ReadFile(exe); string(b) != "old" {
		t.Errorf("binary replaced despite mismatch: %q", b)
	}
	// Again over a previous update's leftover (Windows keeps the old binary
	// aside until RemoveOld).
	arch = archive(t, []byte("newer"))
	sum = sha256.Sum256(arch)
	sums = hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if err := Apply(context.Background(), rel, exe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "newer" {
		t.Errorf("binary = %q", b)
	}
	RemoveOld(exe)
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Errorf("files left next to the binary: %v", entries)
	}
}

func TestArchiveName(t *testing.T) {
	name := ArchiveName("v1.2.3")
	want := ".tar.gz"
	if runtime.GOOS == "windows" {
		want = ".zip"
	}
	if !strings.HasPrefix(name, "automodel_1.2.3_"+runtime.GOOS+"_"+runtime.GOARCH) || !strings.HasSuffix(name, want) {
		t.Errorf("ArchiveName = %s", name)
	}
	// Either format is read, whatever the platform.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("automodel_1.2.3_windows_amd64/automodel.exe")
	w.Write([]byte("win"))
	zw.Close()
	if b, err := extract(buf.Bytes(), "automodel.exe"); err != nil || string(b) != "win" {
		t.Errorf("zip: %q %v", b, err)
	}
}
