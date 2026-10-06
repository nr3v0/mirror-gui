package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

func isYAMLName(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func withYAMLExtension(name string) string {
	if isYAMLName(name) {
		return name
	}
	return name + ".yaml"
}

// truthy follows JavaScript truthiness for decoded YAML values.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case float64:
		return t != 0
	}
	return true
}

// validateImageSetConfig checks that content is an oc-mirror ImageSetConfiguration.
// prefix is prepended to schema errors ("Invalid configuration" or "Invalid YAML").
func validateImageSetConfig(content, prefix string) error {
	var parsed any
	if err := yaml.Unmarshal([]byte(content), &parsed); err != nil {
		return fmt.Errorf("Invalid YAML: %s", err.Error())
	}
	doc, _ := parsed.(map[string]any)
	if kind, _ := doc["kind"].(string); kind != "ImageSetConfiguration" {
		return fmt.Errorf("%s: Must be an ImageSetConfiguration", prefix)
	}
	if apiVersion, _ := doc["apiVersion"].(string); !strings.Contains(apiVersion, "mirror.openshift.io") {
		return fmt.Errorf("%s: Must have mirror.openshift.io API version", prefix)
	}
	if !truthy(doc["mirror"]) {
		return fmt.Errorf("%s: Missing mirror section", prefix)
	}
	return nil
}

// jsonToYAML converts a JSON document to block-style YAML, keeping key order.
func jsonToYAML(raw []byte) (string, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return "", err
	}
	var clearStyle func(n *yaml.Node)
	clearStyle = func(n *yaml.Node) {
		n.Style = 0
		for _, c := range n.Content {
			clearStyle(c)
		}
	}
	clearStyle(&node)
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func (s *Server) handleConfigList(w http.ResponseWriter, _ *http.Request) {
	type configFile struct {
		Name     string `json:"name"`
		Size     string `json:"size"`
		Modified string `json:"modified"`
	}
	entries, err := os.ReadDir(s.cfg.ConfigsDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to list configurations"))
		return
	}
	configs := []configFile{}
	for _, e := range entries {
		if !isYAMLName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		configs = append(configs, configFile{
			Name:     e.Name(),
			Size:     fmt.Sprintf("%.2f KB", float64(info.Size())/1024),
			Modified: isoTime(info.ModTime()),
		})
	}
	writeJSON(w, http.StatusOK, configs)
}

var yamlExtRe = regexp.MustCompile(`(?i)\.ya?ml$`)

func (s *Server) handleConfigDownload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("filename"))
	if name == "" || name == "." || name == "/" || !yamlExtRe.MatchString(name) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid filename"))
		return
	}
	filePath := filepath.Join(s.cfg.ConfigsDir, name)
	f, err := os.Open(filePath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("Configuration file not found"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, errorBody("Configuration file not found"))
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
	w.Header().Set("Content-Type", "application/x-yaml")
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func (s *Server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	raw, body, ok := readBody(w, r)
	if !ok {
		return
	}

	name := ""
	if v, present := body["name"]; present && v != nil && v != "" {
		str, isString := v.(string)
		if !isString {
			writeJSON(w, http.StatusBadRequest, errorBody("name must be a string"))
			return
		}
		if str != filepath.Base(str) {
			writeJSON(w, http.StatusBadRequest, errorBody("Invalid filename"))
			return
		}
		name = str
	}

	var yamlString string
	switch cfg := body["config"].(type) {
	case nil:
		writeJSON(w, http.StatusBadRequest, errorBody("config is required"))
		return
	case string:
		if cfg == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("config is required"))
			return
		}
		yamlString = cfg
	case map[string]any:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("Invalid JSON body"))
			return
		}
		converted, err := jsonToYAML(fields["config"])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("Invalid YAML: "+err.Error()))
			return
		}
		yamlString = converted
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("config must be a YAML string or JSON object"))
		return
	}

	if err := validateImageSetConfig(yamlString, "Invalid configuration"); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	if name == "" {
		name = fmt.Sprintf("imageset-config-%d.yaml", time.Now().UnixMilli())
	}
	filename := withYAMLExtension(name)
	if err := os.WriteFile(filepath.Join(s.cfg.ConfigsDir, filename), []byte(yamlString), 0o644); err != nil {
		log.Printf("Failed to save configuration: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to save configuration"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"message": "Configuration saved successfully", "filename": filename})
}

func (s *Server) handleConfigUpload(w http.ResponseWriter, r *http.Request) {
	_, body, ok := readBody(w, r)
	if !ok {
		return
	}
	filename, _ := body["filename"].(string)
	content, _ := body["content"].(string)
	if filename == "" || content == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("Filename and content are required"))
		return
	}
	if filename != filepath.Base(filename) || hasPathChars(filename) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid filename"))
		return
	}
	if err := validateImageSetConfig(content, "Invalid YAML"); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}

	finalName := withYAMLExtension(filename)
	f, err := os.OpenFile(filepath.Join(s.cfg.ConfigsDir, finalName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		writeJSON(w, http.StatusConflict, errorBody("Configuration file already exists"))
		return
	}
	if err == nil {
		_, err = f.WriteString(content)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		log.Printf("Error uploading configuration: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to upload configuration"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"message": "Configuration uploaded successfully", "filename": finalName})
}

func (s *Server) handleConfigDelete(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if filename == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("Filename is required"))
		return
	}
	if hasPathChars(filename) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid filename"))
		return
	}
	err := os.Remove(filepath.Join(s.cfg.ConfigsDir, filename))
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, errorBody("Configuration file not found"))
		return
	}
	if err != nil {
		log.Printf("Error deleting configuration: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to delete configuration"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"message": "Configuration deleted successfully"})
}
