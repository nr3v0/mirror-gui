// Package ocmirrorbin downloads Red Hat's official oc-mirror binary so it can
// replace the one built into the image at run time.
package ocmirrorbin

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultBaseURL is where Red Hat publishes the latest oc-mirror v2 builds.
const DefaultBaseURL = "https://mirror.openshift.com/pub/cgw/oc-mirror/latest"

// Options configures Fetch.
type Options struct {
	BaseURL  string // directory with sha256sum.txt and the tarballs; default DefaultBaseURL
	Arch     string // Go architecture; default runtime.GOARCH
	CacheDir string // where the binary is kept between restarts
	Client   *http.Client
	Log      func(string)
}

func (o *Options) defaults() {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	o.BaseURL = strings.TrimSuffix(o.BaseURL, "/")
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 10 * time.Minute}
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

func (o *Options) get(ctx context.Context, name string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL, resp.StatusCode)
	}
	return resp, nil
}

// expectedChecksum reads the published SHA-256 of tarball from sha256sum.txt.
func (o *Options) expectedChecksum(ctx context.Context, tarball string) (string, error) {
	resp, err := o.get(ctx, "sha256sum.txt")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == tarball {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("sha256sum.txt has no entry for %s", tarball)
}

// download saves the tarball to a temporary file and returns its path and SHA-256.
func (o *Options) download(ctx context.Context, tarball string) (string, string, error) {
	resp, err := o.get(ctx, tarball)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(o.CacheDir, tarball+".*.tmp")
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary writes the "oc-mirror" entry of a .tar.gz to dest.
func extractBinary(tarballPath, dest string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("tarball does not contain oc-mirror")
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "oc-mirror" {
			continue
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		return err
	}
}

// Version runs `oc-mirror version` to check that the binary works.
func Version(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version", "--output=json").Output()
	if err != nil {
		return "", fmt.Errorf("%s does not run: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Cached returns the binary a previous Fetch downloaded and verified into
// cacheDir, if there is one. It needs no network access.
func Cached(cacheDir string) (string, bool) {
	binary := filepath.Join(cacheDir, "oc-mirror-redhat")
	if _, err := os.Stat(binary + ".sha256"); err != nil {
		return "", false
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return binary, true
}

// Fetch returns the path of Red Hat's current oc-mirror for this architecture.
// It verifies the download against the published sha256sum.txt and keeps the
// binary in CacheDir, downloading again only when Red Hat publishes a new build.
func Fetch(ctx context.Context, opts Options) (string, error) {
	opts.defaults()
	if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
		return "", err
	}
	tarball := fmt.Sprintf("oc-mirror-rhel9-linux-%s.tar.gz", opts.Arch)
	binary := filepath.Join(opts.CacheDir, "oc-mirror-redhat")
	checksumFile := binary + ".sha256"

	expected, err := opts.expectedChecksum(ctx, tarball)
	if err != nil {
		return "", fmt.Errorf("reading published checksums: %w", err)
	}
	if cached, err := os.ReadFile(checksumFile); err == nil && strings.TrimSpace(string(cached)) == expected {
		if _, err := os.Stat(binary); err == nil {
			opts.Log("Using cached Red Hat oc-mirror (" + tarball + " sha256 " + expected[:12] + ")")
			return binary, nil
		}
	}

	opts.Log("Downloading Red Hat oc-mirror: " + opts.BaseURL + "/" + tarball)
	tmpTarball, actual, err := opts.download(ctx, tarball)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", tarball, err)
	}
	defer os.Remove(tmpTarball)
	if actual != expected {
		return "", fmt.Errorf("%s checksum mismatch: got %s, published %s", tarball, actual, expected)
	}

	tmpBinary := binary + ".tmp"
	if err := extractBinary(tmpTarball, tmpBinary); err != nil {
		os.Remove(tmpBinary)
		return "", fmt.Errorf("extracting %s: %w", tarball, err)
	}
	if err := os.Rename(tmpBinary, binary); err != nil {
		return "", err
	}
	if err := os.WriteFile(checksumFile, []byte(expected+"\n"), 0o644); err != nil {
		return "", err
	}
	opts.Log("Red Hat oc-mirror verified (sha256 " + expected[:12] + ")")
	return binary, nil
}
