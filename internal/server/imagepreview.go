package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// An image preview is an operation started with dryRun: oc-mirror runs with
// --dry-run against a throwaway workspace and writes every image it would pull
// to working-dir/dry-run/mapping.txt ("docker://SOURCE=docker://DESTINATION").

// RegistryCount is one registry host plus first path segment, e.g. "registry.redhat.io/redhat".
type RegistryCount struct {
	Prefix string `json:"prefix"`
	Count  int    `json:"count"`
}

func (s *Server) previewWorkspace(id string) string {
	return filepath.Join(s.cfg.StorageDir, "previews", id)
}

func (s *Server) previewImagesPath(id string) string {
	return filepath.Join(s.cfg.LogsDir, id+".images.txt")
}

// freePort returns a TCP port that is currently unused.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// parseMapping returns the de-duplicated, sorted source images of an oc-mirror mapping file.
func parseMapping(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	images := []string{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		source, _, _ := strings.Cut(line, "=")
		source = strings.TrimPrefix(source, "docker://")
		if source != "" && !seen[source] {
			seen[source] = true
			images = append(images, source)
		}
	}
	sort.Strings(images)
	return images, scanner.Err()
}

// registryPrefix reduces an image reference to its registry host and the first
// path segment: "registry.redhat.io/redhat/redhat-operator-index:v4.22" ->
// "registry.redhat.io/redhat". Short Docker Hub names get docker.io/library;
// non-registry sources (e.g. "oci://...") are returned unchanged.
func registryPrefix(ref string) string {
	if strings.Contains(ref, "://") {
		return ref
	}
	if at := strings.Index(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	parts := strings.Split(ref, "/")
	if last := parts[len(parts)-1]; strings.Contains(last, ":") {
		parts[len(parts)-1] = last[:strings.LastIndex(last, ":")]
	}
	host := parts[0]
	if len(parts) == 1 || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		host = "docker.io"
		if len(parts) == 1 {
			parts = []string{host, "library", parts[0]}
		} else {
			parts = append([]string{host}, parts...)
		}
	}
	return host + "/" + parts[1]
}

func countRegistries(images []string) []RegistryCount {
	counts := map[string]int{}
	for _, img := range images {
		counts[registryPrefix(img)]++
	}
	out := make([]RegistryCount, 0, len(counts))
	for prefix, n := range counts {
		out = append(out, RegistryCount{prefix, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// finishPreview collects the dry-run image list into the operation record and
// removes the workspace. It returns an error message when no list was produced.
func (s *Server) finishPreview(id string) (operation, string) {
	workspace := s.previewWorkspace(id)
	defer os.RemoveAll(workspace)

	images, err := parseMapping(filepath.Join(workspace, "working-dir", "dry-run", "mapping.txt"))
	if err != nil {
		return nil, "oc-mirror did not produce a dry-run image list"
	}
	if err := os.WriteFile(s.previewImagesPath(id), []byte(strings.Join(images, "\n")+"\n"), 0o644); err != nil {
		return nil, fmt.Sprintf("saving image list: %v", err)
	}
	return operation{"imageCount": len(images), "imageRegistries": countRegistries(images)}, ""
}

// handleOperationImages returns the images an image preview found. With
// ?download=prefixes or ?download=images it returns a plain-text file instead.
func (s *Server) handleOperationImages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid operation id"))
		return
	}
	data, err := os.ReadFile(s.previewImagesPath(id))
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("No image list for this operation; run an image preview first"))
		return
	}
	images := strings.Fields(string(data))
	registries := countRegistries(images)

	switch r.URL.Query().Get("download") {
	case "prefixes", "images":
		kind := r.URL.Query().Get("download")
		lines := images
		if kind == "prefixes" {
			lines = make([]string, len(registries))
			for i, rc := range registries {
				lines[i] = rc.Prefix
			}
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="image-%s-%s.txt"`, kind, id[:min(8, len(id))]))
		fmt.Fprintln(w, strings.Join(lines, "\n"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{
		"operationId": id, "imageCount": len(images), "registries": registries, "images": images,
	})
}
