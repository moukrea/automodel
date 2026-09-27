// Package update replaces the automodel binary with the latest GitHub
// release, after checking it against the release's checksums.
package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is where releases are published.
const Repo = "moukrea/automodel"

// API is the GitHub API base (overridden in tests).
var API = "https://api.github.com"

type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

var client = &http.Client{Timeout: 2 * time.Minute}

// Latest returns the latest published release.
func Latest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("latest release: %s", resp.Status)
	}
	var r Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Newer reports whether tag is a higher version than current. Development
// builds ("dev") never update themselves.
func Newer(current, tag string) bool {
	c, ok1 := parse(current)
	t, ok2 := parse(tag)
	if !ok1 || !ok2 {
		return false
	}
	for i := range c {
		if t[i] != c[i] {
			return t[i] > c[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ArchiveName is the release asset for this platform: a zip on Windows, a
// tar.gz elsewhere.
func ArchiveName(tag string) string {
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("automodel_%s_%s_%s%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH, ext)
}

// binName is the binary's name in the archive.
func binName() string {
	if runtime.GOOS == "windows" {
		return "automodel.exe"
	}
	return "automodel"
}

// Apply downloads the release archive for this platform, checks its SHA-256
// against checksums.txt, and replaces exe (atomically outside Windows).
func Apply(ctx context.Context, rel *Release, exe string) error {
	name := ArchiveName(rel.Tag)
	var archiveURL, sumsURL string
	for _, a := range rel.Assets {
		switch a.Name {
		case name:
			archiveURL = a.URL
		case "checksums.txt":
			sumsURL = a.URL
		}
	}
	if archiveURL == "" || sumsURL == "" {
		return fmt.Errorf("release %s has no %s or checksums.txt", rel.Tag, name)
	}
	sums, err := fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("checksums.txt has no entry for %s", name)
	}
	archive, err := fetch(ctx, archiveURL, 200<<20)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(archive)
	if hex.EncodeToString(sum[:]) != want {
		return errors.New("checksum mismatch: download refused")
	}
	bin, err := extract(archive, binName())
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(exe), ".automodel.new")
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	return replace(tmp, exe)
}

// RemoveOld deletes the binary a Windows update moved aside, once the
// process that ran it is gone (nothing to do elsewhere).
func RemoveOld(exe string) { os.Remove(exe + ".old") }

func fetch(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, max))
}

func extract(archive []byte, name string) ([]byte, error) {
	if bytes.HasPrefix(archive, []byte("PK\x03\x04")) {
		return extractZip(archive, name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in the archive", name)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == name && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

func extractZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if path.Base(f.Name) != name || f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 200<<20))
	}
	return nil, fmt.Errorf("%s not found in the archive", name)
}
