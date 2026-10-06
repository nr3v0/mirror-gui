// Package catalogtest serves fake operator catalog images from an in-memory
// registry for tests.
package catalogtest

import (
	"archive/tar"
	"bytes"
	"io"
	"io/fs"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// configsLayer builds a layer containing configsDir as /configs.
func configsLayer(t *testing.T, configsDir string) v1.Layer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.WalkDir(configsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(configsDir, path)
		name := filepath.ToSlash(filepath.Join("configs", rel))
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

func platformImage(t *testing.T, layer v1.Layer, arch string) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.ConfigFile(img, &v1.ConfigFile{OS: "linux", Architecture: arch})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// Serve starts an in-memory registry and pushes a multi-arch catalog image
// containing configsDir as /configs for each repo:tag in refs (for example
// "redhat/redhat-operator-index:v4.21"). The arm64 variant has no configs, so
// a test only passes if the linux/amd64 image is selected. It returns the
// registry host and the linux/amd64 manifest digest.
func Serve(t *testing.T, configsDir string, refs ...string) (host, digest string) {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host = strings.TrimPrefix(srv.URL, "http://")

	amd64 := platformImage(t, configsLayer(t, configsDir), "amd64")
	arm64 := platformImage(t, configsLayer(t, t.TempDir()), "arm64")
	index := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: amd64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: arm64, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "arm64"}}},
	)
	for _, ref := range refs {
		tag, err := name.NewTag(host + "/" + ref)
		if err != nil {
			t.Fatal(err)
		}
		if err := remote.WriteIndex(tag, index); err != nil {
			t.Fatal(err)
		}
	}
	d, err := amd64.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return host, d.String()
}
