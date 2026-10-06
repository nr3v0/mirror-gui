package server

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type systemInfo struct {
	OcMirrorVersion    string `json:"ocMirrorVersion"`
	SystemArchitecture string `json:"systemArchitecture"`
	AvailableDiskSpace uint64 `json:"availableDiskSpace"`
	TotalDiskSpace     uint64 `json:"totalDiskSpace"`
	HostDataDir        string `json:"hostDataDir"`
	CacheDir           string `json:"cacheDir"`
	HostCacheDir       string `json:"hostCacheDir"`
	CacheSizeBytes     int64  `json:"cacheSizeBytes"`
}

func runCommand(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func diskSpace(dir string) (available, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return uint64(st.Bavail) * bsize, uint64(st.Blocks) * bsize, nil
}

func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func (s *Server) getSystemInfo() systemInfo {
	version := "Not available"
	if out, err := runCommand("oc-mirror", "version"); err == nil {
		version = parseOcMirrorVersion(strings.TrimSpace(out))
	}
	arch := "Not available"
	if out, err := runCommand("uname", "-m"); err == nil {
		arch = strings.TrimSpace(out)
	}
	available, total, _ := diskSpace(s.cfg.StorageDir)

	hostDataDir := s.cfg.StorageDir
	hostCacheDir := s.cfg.CacheDir
	if s.cfg.HostDataDir != "" {
		hostDataDir = s.cfg.HostDataDir
		if containerDataDir := absPath(s.cfg.StorageDir); strings.HasPrefix(s.cfg.CacheDir, containerDataDir) {
			hostCacheDir = strings.Replace(s.cfg.CacheDir, containerDataDir, hostDataDir, 1)
		}
	}

	return systemInfo{
		OcMirrorVersion:    version,
		SystemArchitecture: arch,
		AvailableDiskSpace: available,
		TotalDiskSpace:     total,
		HostDataDir:        hostDataDir,
		CacheDir:           s.cfg.CacheDir,
		HostCacheDir:       hostCacheDir,
		CacheSizeBytes:     dirSize(s.cfg.CacheDir),
	}
}

const minHealthyDiskBytes = 30_000_000_000

func (s *Server) getSystemHealth() string {
	if _, err := runCommand("oc-mirror", "version"); err != nil {
		return "error"
	}
	if available, _, err := diskSpace(s.cfg.StorageDir); err != nil || available <= minHealthyDiskBytes {
		return "degraded"
	}
	return "healthy"
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jsonObject{
		"status":    "healthy",
		"timestamp": isoTime(time.Now()),
		"service":   "mirror-gui",
	})
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, _ *http.Request) {
	info := s.getSystemInfo()
	health := s.getSystemHealth()
	s.mu.Lock()
	detected := s.pullSecretDetected
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, jsonObject{
		"ocMirrorVersion":    info.OcMirrorVersion,
		"systemHealth":       health,
		"pullSecretDetected": detected,
	})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.getSystemInfo())
}

func (s *Server) handleSystemPaths(w http.ResponseWriter, _ *http.Request) {
	type pathInfo struct {
		Path        string `json:"path"`
		Label       string `json:"label"`
		Description string `json:"description"`
		Available   bool   `json:"available"`
	}
	paths := []pathInfo{
		{Path: s.cfg.DefaultMirrorDir, Label: "Default (Persistent)", Description: "Recommended - primary persistent mirror location"},
		{Path: s.cfg.MirrorBaseDir, Label: "Data Mirrors Root", Description: "Persistent - create subdirectories as needed"},
		{Path: s.cfg.CustomMirrorDir, Label: "Custom Directory", Description: "Persistent - custom subdirectory for this operation"},
		{Path: s.cfg.EphemeralMirrorDir, Label: "App Mirror (Ephemeral)", Description: "Ephemeral mirror path under the app root"},
	}
	for i := range paths {
		paths[i].Available = isPathAvailable(paths[i].Path)
	}
	writeJSON(w, http.StatusOK, jsonObject{"paths": paths})
}

func (s *Server) handleMirrorFoldersList(w http.ResponseWriter, _ *http.Request) {
	folders := []string{}
	if entries, err := os.ReadDir(s.cfg.MirrorBaseDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				folders = append(folders, e.Name())
			}
		}
	}
	sort.Strings(folders)
	writeJSON(w, http.StatusOK, jsonObject{"folders": folders})
}

func (s *Server) handleMirrorFoldersCreate(w http.ResponseWriter, r *http.Request) {
	_, body, ok := readBody(w, r)
	if !ok {
		return
	}
	name, _ := body["name"].(string)
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("Folder name is required"))
		return
	}
	if hasPathChars(trimmed) {
		writeJSON(w, http.StatusBadRequest, errorBody("Folder name cannot contain path separators or traversal characters"))
		return
	}
	if !simpleNameRe.MatchString(trimmed) {
		writeJSON(w, http.StatusBadRequest, errorBody("Use only letters, numbers, dashes, and underscores"))
		return
	}
	folderPath := filepath.Join(s.cfg.MirrorBaseDir, trimmed)
	if err := os.MkdirAll(folderPath, 0o775); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonObject{"error": "Failed to create folder", "details": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"created": trimmed, "path": folderPath})
}

func (s *Server) handleCacheCleanup(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(s.cfg.CacheDir)
	if err == nil {
		for _, e := range entries {
			if err = os.RemoveAll(filepath.Join(s.cfg.CacheDir, e.Name())); err != nil {
				break
			}
		}
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to cleanup cache"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"message": "Cache cleaned up successfully"})
}

// frontendHandler serves the built SPA from DistDir, falling back to index.html
// for client-side routes.
func (s *Server) frontendHandler() http.Handler {
	files := http.FileServer(http.Dir(s.cfg.DistDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeJSON(w, http.StatusNotFound, errorBody("Not found"))
			return
		}
		if p := path.Clean(r.URL.Path); p != "/" {
			if info, err := os.Stat(filepath.Join(s.cfg.DistDir, filepath.FromSlash(p))); err == nil && !info.IsDir() {
				w.Header().Set("Cache-Control", "public, max-age=86400")
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := os.Open(filepath.Join(s.cfg.DistDir, "index.html"))
		if err != nil {
			http.Error(w, "Frontend not built: run `npm run build` to create dist/", http.StatusNotFound)
			return
		}
		defer index.Close()
		info, err := index.Stat()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", info.ModTime(), index)
	})
}
