package update

import (
	"archive/tar"
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

func archive(t *testing.T, content []byte) []byte {
	var buf bytes.Buffer
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
}
