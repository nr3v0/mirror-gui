package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nr3v0/mirror-gui/internal/ocmirrorbin"
)

// New operations run one of two oc-mirror binaries:
//
//	built-in  the binary in the image (Config.OcMirrorPath), compiled from source
//	          or Red Hat's build, as recorded in Config.OcMirrorSource
//	redhat    Red Hat's official binary, downloaded from mirror.openshift.com,
//	          verified against the published checksum and cached in data/bin
//
// The choice is made on the Dashboard and saved in data/oc-mirror.json; without
// a saved choice, OC_MIRROR_BINARY decides at start-up. Operations that are
// already running keep the binary they started with.
const (
	ocMirrorBuiltIn = "built-in"
	ocMirrorRedHat  = "redhat"

	// ocMirrorRedHatSource is the ocMirrorSource reported for the downloaded binary.
	ocMirrorRedHatSource = "redhat-runtime"
	ocMirrorFetchTimeout = 15 * time.Minute
)

var errOcMirrorSwitching = errors.New("the oc-mirror binary is already being changed")

type ocMirrorPreference struct {
	Binary string `json:"binary"`
}

func (s *Server) ocMirrorPreferencePath() string {
	return filepath.Join(s.cfg.StorageDir, "oc-mirror.json")
}

func (s *Server) ocMirrorCacheDir() string {
	return absPath(filepath.Join(s.cfg.StorageDir, "bin"))
}

// ocMirrorBinary returns the oc-mirror executable new operations use and where it came from.
func (s *Server) ocMirrorBinary() (path, source string) {
	s.ocMirrorMu.Lock()
	defer s.ocMirrorMu.Unlock()
	return s.ocMirrorPath, s.ocMirrorSource
}

func (s *Server) setOcMirror(choice, path, source string) {
	s.ocMirrorMu.Lock()
	defer s.ocMirrorMu.Unlock()
	s.ocMirrorChoice, s.ocMirrorPath, s.ocMirrorSource = choice, path, source
}

// fetchRedHatOcMirror downloads Red Hat's oc-mirror, or reuses the cached copy
// when it is still the published build, and checks that it runs. When the
// download site cannot be reached (e.g. a disconnected host), a binary that an
// earlier download verified is used instead.
func (s *Server) fetchRedHatOcMirror(ctx context.Context) (string, error) {
	cacheDir := s.ocMirrorCacheDir()
	path, err := ocmirrorbin.Fetch(ctx, ocmirrorbin.Options{
		BaseURL:  s.cfg.OcMirrorRedHatURL,
		CacheDir: cacheDir,
		Log:      func(line string) { log.Print(line) },
	})
	if err != nil {
		cached, ok := ocmirrorbin.Cached(cacheDir)
		if !ok {
			return "", err
		}
		log.Printf("WARNING: %v; using the Red Hat oc-mirror downloaded earlier", err)
		path = cached
	}
	if _, err := ocmirrorbin.Version(ctx, path); err != nil {
		return "", err
	}
	return path, nil
}

// selectOcMirror makes new operations use choice, downloading Red Hat's binary
// first when needed. On error the current binary stays selected.
func (s *Server) selectOcMirror(ctx context.Context, choice string) error {
	switch choice {
	case ocMirrorBuiltIn:
		s.setOcMirror(ocMirrorBuiltIn, s.cfg.OcMirrorPath, s.cfg.OcMirrorSource)
	case ocMirrorRedHat:
		path, err := s.fetchRedHatOcMirror(ctx)
		if err != nil {
			return err
		}
		s.setOcMirror(ocMirrorRedHat, path, ocMirrorRedHatSource)
		log.Printf("Using Red Hat oc-mirror: %s", path)
	default:
		return fmt.Errorf("unknown oc-mirror binary %q (expected %q or %q)", choice, ocMirrorBuiltIn, ocMirrorRedHat)
	}
	return nil
}

// InitOcMirror selects the oc-mirror binary at start-up: the choice saved from
// the Dashboard or, without one, envChoice (OC_MIRROR_BINARY). If Red Hat's
// binary cannot be fetched, the built-in one is used and a warning is logged;
// the saved choice is kept, so the next start tries again.
func (s *Server) InitOcMirror(envChoice string) {
	choice := envChoice
	if choice == "" {
		choice = ocMirrorBuiltIn
	}
	if choice != ocMirrorBuiltIn && choice != ocMirrorRedHat {
		log.Printf("WARNING: ignoring OC_MIRROR_BINARY=%q (expected %q or %q)", envChoice, ocMirrorRedHat, ocMirrorBuiltIn)
		choice = ocMirrorBuiltIn
	}
	if data, err := os.ReadFile(s.ocMirrorPreferencePath()); err == nil {
		var pref ocMirrorPreference
		if err := json.Unmarshal(data, &pref); err == nil && (pref.Binary == ocMirrorBuiltIn || pref.Binary == ocMirrorRedHat) {
			choice = pref.Binary
		} else {
			log.Printf("WARNING: ignoring unreadable %s", s.ocMirrorPreferencePath())
		}
	}
	if choice == ocMirrorBuiltIn {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ocMirrorFetchTimeout)
	defer cancel()
	if err := s.selectOcMirror(ctx, choice); err != nil {
		log.Printf("WARNING: Red Hat oc-mirror: %v; using the built-in oc-mirror", err)
	}
}

func (s *Server) ocMirrorStatus() jsonObject {
	s.ocMirrorMu.Lock()
	choice, path, source, switching := s.ocMirrorChoice, s.ocMirrorPath, s.ocMirrorSource, s.ocMirrorSwitching
	s.ocMirrorMu.Unlock()

	version := "Not available"
	if out, err := runCommand(path, "version"); err == nil {
		version = parseOcMirrorVersion(strings.TrimSpace(out))
	}
	_, downloaded := ocmirrorbin.Cached(s.ocMirrorCacheDir())
	return jsonObject{
		"selected":         choice,
		"source":           source,
		"version":          version,
		"builtInSource":    s.cfg.OcMirrorSource,
		"redhatDownloaded": downloaded,
		"switching":        switching,
	}
}

func (s *Server) handleOcMirrorGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.ocMirrorStatus())
}

// handleOcMirrorSelect switches the binary for new operations. Selecting Red
// Hat's binary may download it first, so the request can take a minute; the
// download is not cancelled if the client goes away.
func (s *Server) handleOcMirrorSelect(w http.ResponseWriter, r *http.Request) {
	var body ocMirrorPreference
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid request body"))
		return
	}
	if body.Binary != ocMirrorBuiltIn && body.Binary != ocMirrorRedHat {
		writeJSON(w, http.StatusBadRequest, errorBody(fmt.Sprintf("binary must be %q or %q", ocMirrorBuiltIn, ocMirrorRedHat)))
		return
	}

	s.ocMirrorMu.Lock()
	if s.ocMirrorSwitching {
		s.ocMirrorMu.Unlock()
		writeJSON(w, http.StatusConflict, errorBody(errOcMirrorSwitching.Error()))
		return
	}
	s.ocMirrorSwitching = true
	s.ocMirrorMu.Unlock()
	defer func() {
		s.ocMirrorMu.Lock()
		s.ocMirrorSwitching = false
		s.ocMirrorMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), ocMirrorFetchTimeout)
	defer cancel()
	if err := s.selectOcMirror(ctx, body.Binary); err != nil {
		writeJSON(w, http.StatusBadGateway, errorBody("Could not use Red Hat's oc-mirror: "+err.Error()))
		return
	}
	data, _ := json.Marshal(body)
	if err := os.WriteFile(s.ocMirrorPreferencePath(), append(data, '\n'), 0o644); err != nil {
		log.Printf("WARNING: saving the oc-mirror choice: %v", err)
	}
	writeJSON(w, http.StatusOK, s.ocMirrorStatus())
}
