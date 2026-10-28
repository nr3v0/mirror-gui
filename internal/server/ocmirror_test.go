package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRedHatOcMirror answers `version` like oc-mirror and otherwise succeeds,
// so operations can run it.
const fakeRedHatOcMirror = `#!/bin/sh
case "$1" in
version) echo '{"clientVersion":{"gitVersion":"9.9.9"}}' ;;
*) echo "red hat oc-mirror ran" ;;
esac
`

type fakeDownloadSite struct {
	*httptest.Server
	downloads atomic.Int32
}

// serveRedHatOcMirror publishes fakeRedHatOcMirror for this machine's
// architecture, the way mirror.openshift.com publishes oc-mirror.
func serveRedHatOcMirror(t *testing.T) *fakeDownloadSite {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "oc-mirror", Mode: 0o755, Size: int64(len(fakeRedHatOcMirror)), Typeflag: tar.TypeReg})
	tw.Write([]byte(fakeRedHatOcMirror))
	tw.Close()
	gz.Close()
	body := buf.Bytes()
	sum := sha256.Sum256(body)
	tarball := fmt.Sprintf("oc-mirror-rhel9-linux-%s.tar.gz", runtime.GOARCH)

	site := &fakeDownloadSite{}
	site.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sha256sum.txt":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), tarball)
		case "/" + tarball:
			site.downloads.Add(1)
			w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(site.Close)
	return site
}

func TestOcMirrorSelectionDefaultsToBuiltIn(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"v2.0.0-source-c4d52fae"}}'`)
	e.srv.cfg.OcMirrorSource = "source"
	body := e.do("GET", "/api/system/oc-mirror", nil).obj()
	if body["selected"] != "built-in" || body["version"] != "2.0.0" || body["redhatDownloaded"] != false || body["switching"] != false {
		t.Errorf("status = %v", body)
	}
	if body["builtInSource"] != "source" {
		t.Errorf("builtInSource = %v", body["builtInSource"])
	}
}

func TestOcMirrorSelectRedHatDownloadsAndOperationsUseIt(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"2.0.0"}}'; [ "$1" = version ] || exit 1`)
	site := serveRedHatOcMirror(t)
	e.srv.cfg.OcMirrorRedHatURL = site.URL

	res := e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"})
	e.expectStatus(res, 200)
	body := res.obj()
	if body["selected"] != "redhat" || body["source"] != "redhat-runtime" || body["version"] != "9.9.9" || body["redhatDownloaded"] != true {
		t.Errorf("status = %v", body)
	}
	if site.downloads.Load() != 1 {
		t.Errorf("downloads = %d, want 1", site.downloads.Load())
	}
	saved, _ := os.ReadFile(filepath.Join(e.cfg.StorageDir, "oc-mirror.json"))
	if strings.TrimSpace(string(saved)) != `{"binary":"redhat"}` {
		t.Errorf("saved preference = %q", saved)
	}
	info := e.do("GET", "/api/system/info", nil).obj()
	if info["ocMirrorVersion"] != "9.9.9" || info["ocMirrorSource"] != "redhat-runtime" {
		t.Errorf("info = %v", info)
	}

	// The built-in fake fails anything but `version`, so success means Red Hat's ran.
	id := e.startOperation(map[string]any{})
	if op := e.waitForStatus(id, "success", "failed"); op["status"] != "success" {
		t.Errorf("operation = %v (ran the built-in oc-mirror?)", op)
	}

	// Selecting it again reuses the verified download.
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "built-in"}), 200)
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"}), 200)
	if site.downloads.Load() != 1 {
		t.Errorf("downloads = %d after reselecting, want 1", site.downloads.Load())
	}
}

func TestOcMirrorSelectBuiltInSwitchesBack(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"2.0.0"}}'`)
	e.srv.cfg.OcMirrorRedHatURL = serveRedHatOcMirror(t).URL
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"}), 200)

	res := e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "built-in"})
	e.expectStatus(res, 200)
	if body := res.obj(); body["selected"] != "built-in" || body["version"] != "2.0.0" || body["redhatDownloaded"] != true {
		t.Errorf("status = %v", body)
	}
	if path, _ := e.srv.ocMirrorBinary(); path != "oc-mirror" {
		t.Errorf("operations would run %q", path)
	}
}

func TestOcMirrorSelectRedHatOfflineUsesEarlierDownload(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"2.0.0"}}'`)
	site := serveRedHatOcMirror(t)
	e.srv.cfg.OcMirrorRedHatURL = site.URL
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"}), 200)
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "built-in"}), 200)

	site.Close() // the download site is now unreachable
	res := e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"})
	e.expectStatus(res, 200)
	if body := res.obj(); body["selected"] != "redhat" || body["version"] != "9.9.9" {
		t.Errorf("status = %v", body)
	}
}

func TestOcMirrorSelectRedHatFailureKeepsCurrentBinary(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"2.0.0"}}'`)
	unreachable := httptest.NewServer(http.NotFoundHandler())
	unreachable.Close()
	e.srv.cfg.OcMirrorRedHatURL = unreachable.URL

	res := e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "redhat"})
	e.expectStatus(res, 502)
	if !strings.Contains(errorText(res), "Could not use Red Hat's oc-mirror") {
		t.Errorf("error = %q", errorText(res))
	}
	if body := e.do("GET", "/api/system/oc-mirror", nil).obj(); body["selected"] != "built-in" || body["switching"] != false {
		t.Errorf("status after failure = %v", body)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.StorageDir, "oc-mirror.json")); !os.IsNotExist(err) {
		t.Errorf("a failed switch saved the choice: %v", err)
	}
}

func TestOcMirrorSelectValidation(t *testing.T) {
	e := newTestEnv(t)
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "latest"}), 400)
	e.expectStatus(e.do("PUT", "/api/system/oc-mirror", nil), 400)

	e.srv.ocMirrorSwitching = true
	res := e.do("PUT", "/api/system/oc-mirror", map[string]any{"binary": "built-in"})
	e.expectStatus(res, 409)
	if body := e.do("GET", "/api/system/oc-mirror", nil).obj(); body["switching"] != true {
		t.Errorf("status = %v", body)
	}
}

func TestInitOcMirror(t *testing.T) {
	installFakeOcMirror(t, `echo '{"clientVersion":{"gitVersion":"2.0.0"}}'`)
	site := serveRedHatOcMirror(t)

	t.Run("OC_MIRROR_BINARY=redhat without a saved choice", func(t *testing.T) {
		e := newTestEnv(t)
		e.srv.cfg.OcMirrorRedHatURL = site.URL
		e.srv.InitOcMirror("redhat")
		if _, source := e.srv.ocMirrorBinary(); source != "redhat-runtime" {
			t.Errorf("source = %q", source)
		}
	})

	t.Run("the saved choice overrides OC_MIRROR_BINARY", func(t *testing.T) {
		e := newTestEnv(t)
		e.srv.cfg.OcMirrorRedHatURL = site.URL
		os.WriteFile(filepath.Join(e.cfg.StorageDir, "oc-mirror.json"), []byte(`{"binary":"built-in"}`), 0o644)
		e.srv.InitOcMirror("redhat")
		if path, _ := e.srv.ocMirrorBinary(); path != "oc-mirror" {
			t.Errorf("path = %q", path)
		}

		os.WriteFile(filepath.Join(e.cfg.StorageDir, "oc-mirror.json"), []byte(`{"binary":"redhat"}`), 0o644)
		e.srv.InitOcMirror("")
		if _, source := e.srv.ocMirrorBinary(); source != "redhat-runtime" {
			t.Errorf("source = %q", source)
		}
	})

	t.Run("a failed download falls back to the built-in binary", func(t *testing.T) {
		e := newTestEnv(t)
		e.srv.cfg.OcMirrorRedHatURL = site.URL + "/missing"
		e.srv.InitOcMirror("redhat")
		if path, _ := e.srv.ocMirrorBinary(); path != "oc-mirror" {
			t.Errorf("path = %q", path)
		}
	})

	t.Run("unknown OC_MIRROR_BINARY is ignored", func(t *testing.T) {
		e := newTestEnv(t)
		e.srv.InitOcMirror("nightly")
		if path, _ := e.srv.ocMirrorBinary(); path != "oc-mirror" {
			t.Errorf("path = %q", path)
		}
	})
}
