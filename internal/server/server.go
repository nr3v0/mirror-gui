// Package server implements the mirror-gui HTTP API and serves the built frontend.
package server

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nr3v0/mirror-gui/internal/catalogmeta"
)

// Config holds filesystem locations and settings, normally read from the environment.
type Config struct {
	Port               string
	StorageDir         string
	ConfigsDir         string
	OperationsDir      string
	LogsDir            string
	CacheDir           string
	AppRootDir         string
	MirrorBaseDir      string
	DefaultMirrorDir   string
	CustomMirrorDir    string
	EphemeralMirrorDir string
	AuthfilePath       string
	RuntimeCatalogDir  string
	BuiltinCatalogDir  string
	DistDir            string
	HostDataDir        string
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// ConfigFromEnv builds a Config from the same environment variables the Node server used.
// The app root (catalog-data/, dist/, sync-catalogs.sh) defaults to the working directory.
func ConfigFromEnv() Config {
	appRoot := os.Getenv("OC_MIRROR_WORKDIR")
	if appRoot == "" {
		appRoot, _ = os.Getwd()
	}
	storage := envOr("STORAGE_DIR", "./data")
	mirrorBase := absPath(envOr("OC_MIRROR_BASE_MIRROR_DIR", filepath.Join(storage, "mirrors")))
	return Config{
		Port:               envOr("PORT", "3001"),
		StorageDir:         storage,
		ConfigsDir:         filepath.Join(storage, "configs"),
		OperationsDir:      filepath.Join(storage, "operations"),
		LogsDir:            filepath.Join(storage, "logs"),
		CacheDir:           absPath(envOr("OC_MIRROR_CACHE_DIR", filepath.Join(storage, "cache"))),
		AppRootDir:         appRoot,
		MirrorBaseDir:      mirrorBase,
		DefaultMirrorDir:   filepath.Join(mirrorBase, "default"),
		CustomMirrorDir:    filepath.Join(mirrorBase, "custom"),
		EphemeralMirrorDir: absPath(envOr("OC_MIRROR_EPHEMERAL_DIR", filepath.Join(appRoot, "mirror"))),
		AuthfilePath:       envOr("OC_MIRROR_AUTHFILE", "/app/pull-secret.json"),
		RuntimeCatalogDir:  filepath.Join(storage, "catalog-data"),
		BuiltinCatalogDir:  filepath.Join(appRoot, "catalog-data"),
		DistDir:            filepath.Join(appRoot, "dist"),
		HostDataDir:        os.Getenv("HOST_DATA_DIR"),
	}
}

type runningProcess struct {
	proc *os.Process
	done chan struct{}
}

// Server holds all in-memory state. It is an http.Handler.
type Server struct {
	cfg     Config
	handler http.Handler
	client  *http.Client

	mu                 sync.Mutex // guards the fields below
	pullSecretPath     string
	pullSecretDetected bool
	running            map[string]*runningProcess
	stopped            map[string]bool
	registryCache      map[string]registryStatus

	opsMu sync.Mutex // serializes read-modify-write of operation records

	catalogMu sync.Mutex // guards catalog data and caches
	catalog   *catalogData
	deps      map[string]map[string][]OperatorDependency
	opCache   operatorCache

	syncMu    sync.Mutex
	syncState catalogSyncState

	// Catalogs synced by POST /api/catalogs/sync, the registry they are pulled
	// from (empty means registry.redhat.io), and the delay between retries.
	syncRegistry     string
	syncVersions     []string
	syncCatalogTypes []string
	syncRetryDelay   time.Duration

	// Test-only hooks that force the next request on a route to fail.
	failNextCatalogsGet  bool
	failNextOperatorsGet bool
}

// New creates the storage directories, detects the pull secret and registers routes.
func New(cfg Config) *Server {
	s := &Server{
		cfg:           cfg,
		client:        &http.Client{Timeout: 10 * time.Second},
		running:       map[string]*runningProcess{},
		stopped:       map[string]bool{},
		registryCache: map[string]registryStatus{},

		syncVersions:     catalogmeta.DefaultOCPVersions,
		syncCatalogTypes: catalogmeta.DefaultCatalogTypes,
		syncRetryDelay:   2 * time.Second,
	}
	s.syncState = s.newCatalogSyncState()
	s.ensureDirectories()
	s.detectPullSecret()
	s.handler = s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }

	handle("GET /api/stats", s.handleStats)
	handle("GET /api/health", s.handleHealth)
	handle("GET /api/system/status", s.handleSystemStatus)
	handle("GET /api/system/info", s.handleSystemInfo)
	handle("GET /api/system/paths", s.handleSystemPaths)

	handle("GET /api/pull-secret/status", s.handlePullSecretStatus)
	handle("GET /api/pull-secret/content", s.handlePullSecretContent)
	handle("POST /api/pull-secret", s.handlePullSecretSave)
	handle("DELETE /api/pull-secret", s.handlePullSecretDelete)
	handle("GET /api/registries", s.handleRegistries)
	handle("POST /api/registries/verify", s.handleRegistryVerify)

	handle("GET /api/mirror-folders", s.handleMirrorFoldersList)
	handle("POST /api/mirror-folders", s.handleMirrorFoldersCreate)

	handle("GET /api/config/list", s.handleConfigList)
	handle("GET /api/config/download/{filename}", s.handleConfigDownload)
	handle("POST /api/config/save", s.handleConfigSave)
	handle("POST /api/config/upload", s.handleConfigUpload)
	handle("DELETE /api/config/delete/{filename}", s.handleConfigDelete)

	handle("GET /api/channels", s.handleChannels)
	handle("GET /api/catalogs", s.handleCatalogs)
	handle("GET /api/operators", s.handleOperators)
	handle("POST /api/operators/refresh-cache", s.handleOperatorsRefresh)
	handle("GET /api/operators/channels", s.handleOperatorsChannels)
	handle("GET /api/operators/{operator}/versions", s.handleOperatorVersions)
	handle("GET /api/operators/{operator}/dependencies", s.handleOperatorDependencies)
	handle("GET /api/operator-channels/{operator}", s.handleOperatorChannels)

	handle("GET /api/operations", s.handleOperationsList)
	handle("GET /api/operations/history", s.handleOperationsList)
	handle("GET /api/operations/recent", s.handleOperationsRecent)
	handle("POST /api/operations/start", s.handleOperationStart)
	handle("POST /api/operations/{id}/stop", s.handleOperationStop)
	handle("DELETE /api/operations/{id}", s.handleOperationDelete)
	handle("GET /api/operations/{id}/logs", s.handleOperationLogs)
	handle("GET /api/operations/{id}/details", s.handleOperationDetails)
	handle("GET /api/operations/{id}/logstream", s.handleOperationLogStream)

	handle("POST /api/cache/cleanup", s.handleCacheCleanup)
	handle("POST /api/catalogs/sync", s.handleCatalogSync)
	handle("GET /api/catalogs/sync/status", s.handleCatalogSyncStatus)
	handle("DELETE /api/catalogs/sync/data", s.handleCatalogSyncDelete)

	handle("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody("Not found"))
	})
	mux.Handle("/", s.frontendHandler())

	return logRequests(cors(mux))
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

// cors mirrors the permissive defaults of the Express cors() middleware.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE")
			if h := r.Header.Get("Access-Control-Request-Headers"); h != "" {
				w.Header().Set("Access-Control-Allow-Headers", h)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type jsonObject = map[string]any

func errorBody(msg string) jsonObject { return jsonObject{"error": msg} }

func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("Error encoding JSON response: %v", err)
		http.Error(w, `{"error":"Internal server error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

const maxBodyBytes = 10 << 20

// readBody reads a JSON object body. An empty body yields an empty object.
// On failure it writes a 400/413 response and returns ok=false.
func readBody(w http.ResponseWriter, r *http.Request) (raw []byte, body jsonObject, ok bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, errorBody("Request body too large"))
		return nil, nil, false
	}
	body = jsonObject{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, body, true
	}
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid JSON body"))
		return nil, nil, false
	}
	return raw, body, true
}

// isoTime formats like JavaScript's Date.toISOString().
func isoTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

var simpleNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func hasPathChars(name string) bool {
	return strings.Contains(name, "/") || strings.Contains(name, "..") || strings.Contains(name, `\`)
}

func (s *Server) ensureDirectories() {
	for _, dir := range []string{
		s.cfg.StorageDir, s.cfg.ConfigsDir, s.cfg.OperationsDir, s.cfg.LogsDir,
		s.cfg.CacheDir, s.cfg.MirrorBaseDir, s.cfg.DefaultMirrorDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("Error creating directory %s: %v", dir, err)
		}
	}
}

// ClearOperationHistory removes operation records and logs on a fresh start,
// i.e. when the default mirror directory is empty. Persistent mirrors keep history.
func (s *Server) ClearOperationHistory() {
	if entries, err := os.ReadDir(s.cfg.DefaultMirrorDir); err == nil && len(entries) > 0 {
		log.Println("Persistent mirror files detected - keeping operation history")
		return
	}
	clearedOps, clearedLogs := 0, 0
	if entries, err := os.ReadDir(s.cfg.OperationsDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") && os.Remove(filepath.Join(s.cfg.OperationsDir, e.Name())) == nil {
				clearedOps++
			}
		}
	}
	if entries, err := os.ReadDir(s.cfg.LogsDir); err == nil {
		for _, e := range entries {
			if os.Remove(filepath.Join(s.cfg.LogsDir, e.Name())) == nil {
				clearedLogs++
			}
		}
	}
	if clearedOps > 0 || clearedLogs > 0 {
		log.Printf("Cleared %d operation files and %d log files on startup (fresh start detected)", clearedOps, clearedLogs)
	}
}

// LogStartup prints the effective configuration.
func (s *Server) LogStartup() {
	c := s.cfg
	log.Printf("Mirror-GUI server running on port %s", c.Port)
	log.Printf("Storage directory: %s", c.StorageDir)
	log.Printf("Configs directory: %s", c.ConfigsDir)
	log.Printf("Operations directory: %s", c.OperationsDir)
	log.Printf("Logs directory: %s", c.LogsDir)
	log.Printf("Cache directory: %s", c.CacheDir)
	log.Printf("App root directory: %s", c.AppRootDir)
	log.Printf("Mirror base directory: %s", c.MirrorBaseDir)
	log.Printf("Authfile path: %s", c.AuthfilePath)
	log.Printf("Frontend directory: %s", c.DistDir)
	log.Printf("Health check: http://localhost:%s/api/health", c.Port)
}
