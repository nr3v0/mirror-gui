package catalogmeta

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// catalogPlatform is the image platform catalog configs are read from. The
// configs are identical across architectures; this matches the digest that
// `oc image info --filter-by-os=linux/amd64` used to record.
var catalogPlatform = v1.Platform{OS: "linux", Architecture: "amd64"}

// authFileKeychain resolves credentials from a docker/podman auth file
// ({"auths": {"registry": {"auth": "base64(user:pass)"}}}).
type authFileKeychain struct {
	auths map[string]authn.AuthConfig
}

func loadAuthFile(path string) (*authFileKeychain, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Auths map[string]authn.AuthConfig `json:"auths"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &authFileKeychain{auths: file.Auths}, nil
}

func (k *authFileKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	host := target.RegistryStr()
	for key, cfg := range k.auths {
		// Keys may be "host", "https://host" or "https://host/v1/".
		normalized := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(key, "https://"), "http://"), "/")
		normalized = strings.TrimSuffix(normalized, "/v1")
		if normalized == host {
			return authn.FromConfig(cfg), nil
		}
	}
	return authn.Anonymous, nil
}

// keychain returns credentials from RegistryConfig, or the default
// docker/podman credential locations when no auth file is configured.
func (o *SyncOptions) keychain() (authn.Keychain, error) {
	if o.RegistryConfig == "" {
		return authn.DefaultKeychain, nil
	}
	return loadAuthFile(o.RegistryConfig)
}

// pullImage resolves a catalog image for catalogPlatform.
func (o *SyncOptions) pullImage(ctx context.Context, ref string) (v1.Image, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, err
	}
	keychain, err := o.keychain()
	if err != nil {
		return nil, err
	}
	return remote.Image(parsed,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(keychain),
		remote.WithPlatform(catalogPlatform),
	)
}

// extractConfigs writes the image's /configs directory to dest, applying layer
// whiteouts. Only directories and regular files are extracted.
func extractConfigs(img v1.Image, dest string) error {
	rc := mutate.Extract(img)
	defer rc.Close()

	tr := tar.NewReader(rc)
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		clean := strings.TrimPrefix(path.Clean("/"+hdr.Name), "/")
		rel, ok := strings.CutPrefix(clean, "configs/")
		if !ok {
			continue
		}
		found = true
		target := filepath.Join(dest, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
		}
	}
	if !found {
		return errors.New("image has no /configs directory")
	}
	return nil
}
