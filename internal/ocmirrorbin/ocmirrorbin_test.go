package ocmirrorbin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const fakeBinary = "#!/bin/sh\necho '{\"clientVersion\":{\"gitVersion\":\"2.0.0-redhat\"}}'\n"

func tarball(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(content))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type mirror struct {
	*httptest.Server
	downloads atomic.Int32
}

// serve publishes a tarball for amd64 plus a sha256sum.txt; checksum overrides the published hash.
func serve(t *testing.T, body []byte, checksum string) *mirror {
	t.Helper()
	sum := sha256.Sum256(body)
	if checksum == "" {
		checksum = hex.EncodeToString(sum[:])
	}
	m := &mirror{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sha256sum.txt":
			w.Write([]byte("0000  oc-mirror-rhel8-linux-amd64.tar.gz\n" + checksum + "  oc-mirror-rhel9-linux-amd64.tar.gz\n"))
		case "/oc-mirror-rhel9-linux-amd64.tar.gz":
			m.downloads.Add(1)
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(m.Close)
	return m
}

func TestFetchDownloadsVerifiesAndCaches(t *testing.T) {
	m := serve(t, tarball(t, "oc-mirror", fakeBinary), "")
	cache := filepath.Join(t.TempDir(), "bin")
	var logs []string
	opts := Options{BaseURL: m.URL + "/", Arch: "amd64", CacheDir: cache, Log: func(l string) { logs = append(logs, l) }}

	path, err := Fetch(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(cache, "oc-mirror-redhat") {
		t.Errorf("path = %s", path)
	}
	if out, err := Version(context.Background(), path); err != nil || !strings.Contains(out, "2.0.0-redhat") {
		t.Errorf("Version = %q, %v", out, err)
	}

	// A second start reuses the cached binary while the published checksum is unchanged.
	if _, err := Fetch(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if n := m.downloads.Load(); n != 1 {
		t.Errorf("downloads = %d, want 1", n)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "Using cached Red Hat oc-mirror") {
		t.Errorf("logs = %v", logs)
	}
	if entries, _ := filepath.Glob(filepath.Join(cache, "*.tmp")); len(entries) != 0 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}

func TestFetchRedownloadsWhenPublishedBuildChanges(t *testing.T) {
	cache := t.TempDir()
	first := serve(t, tarball(t, "oc-mirror", fakeBinary), "")
	if _, err := Fetch(context.Background(), Options{BaseURL: first.URL, Arch: "amd64", CacheDir: cache}); err != nil {
		t.Fatal(err)
	}
	second := serve(t, tarball(t, "./oc-mirror", strings.Replace(fakeBinary, "2.0.0-redhat", "2.0.1-redhat", 1)), "")
	path, err := Fetch(context.Background(), Options{BaseURL: second.URL, Arch: "amd64", CacheDir: cache})
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := Version(context.Background(), path); !strings.Contains(out, "2.0.1-redhat") || second.downloads.Load() != 1 {
		t.Errorf("version = %q, downloads = %d", out, second.downloads.Load())
	}
}

func TestFetchRejectsChecksumMismatch(t *testing.T) {
	m := serve(t, tarball(t, "oc-mirror", fakeBinary), strings.Repeat("ab", 32))
	cache := t.TempDir()
	_, err := Fetch(context.Background(), Options{BaseURL: m.URL, Arch: "amd64", CacheDir: cache})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "oc-mirror-redhat")); !os.IsNotExist(err) {
		t.Error("unverified binary was installed")
	}
}

func TestFetchErrors(t *testing.T) {
	m := serve(t, tarball(t, "oc-mirror", fakeBinary), "")
	if _, err := Fetch(context.Background(), Options{BaseURL: m.URL, Arch: "riscv64", CacheDir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "no entry for oc-mirror-rhel9-linux-riscv64.tar.gz") {
		t.Errorf("unknown arch err = %v", err)
	}
	empty := serve(t, tarball(t, "README", "no binary here"), "")
	if _, err := Fetch(context.Background(), Options{BaseURL: empty.URL, Arch: "amd64", CacheDir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "does not contain oc-mirror") {
		t.Errorf("missing binary err = %v", err)
	}
	if _, err := Fetch(context.Background(), Options{BaseURL: m.URL + "/nope", Arch: "amd64", CacheDir: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("404 err = %v", err)
	}
}
