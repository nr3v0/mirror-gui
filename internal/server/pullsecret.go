package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
)

type registryStatus struct {
	Status string
	Error  string
}

type pullSecret struct {
	Auths map[string]struct {
		Auth string `json:"auth"`
	} `json:"auths"`
}

func (s *Server) detectPullSecret() {
	s.mu.Lock()
	defer s.mu.Unlock()
	content, err := os.ReadFile(s.cfg.AuthfilePath)
	if err == nil && len(strings.TrimSpace(string(content))) > 2 {
		s.pullSecretPath, s.pullSecretDetected = s.cfg.AuthfilePath, true
		log.Printf("Pull secret detected at: %s", s.cfg.AuthfilePath)
		return
	}
	s.pullSecretPath, s.pullSecretDetected = "", false
	log.Println("No pull secret detected")
}

// currentPullSecretPath returns the configured pull secret path, or "" if none.
func (s *Server) currentPullSecretPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pullSecretDetected {
		return ""
	}
	return s.pullSecretPath
}

// readPullSecret parses the configured pull secret and also returns its
// registry hosts in file order. It returns a nil secret when none is configured.
func (s *Server) readPullSecret() (*pullSecret, []string, error) {
	p := s.currentPullSecretPath()
	if p == "" {
		return nil, nil, nil
	}
	content, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	var ps pullSecret
	if err := json.Unmarshal(content, &ps); err != nil {
		return nil, nil, err
	}
	return &ps, orderedAuthHosts(content), nil
}

func (s *Server) handlePullSecretStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var p any
	if s.pullSecretPath != "" {
		p = s.pullSecretPath
	}
	writeJSON(w, http.StatusOK, jsonObject{"detected": s.pullSecretDetected, "path": p})
}

func (s *Server) handlePullSecretContent(w http.ResponseWriter, _ *http.Request) {
	content := ""
	if p := s.currentPullSecretPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			content = string(b)
		}
	}
	writeJSON(w, http.StatusOK, jsonObject{"content": content})
}

func (s *Server) handlePullSecretSave(w http.ResponseWriter, r *http.Request) {
	_, body, ok := readBody(w, r)
	if !ok {
		return
	}
	content, isString := body["content"].(string)
	if !isString || len(strings.TrimSpace(content)) < 2 {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid pull secret content"))
		return
	}
	if !json.Valid([]byte(content)) {
		writeJSON(w, http.StatusBadRequest, errorBody("Pull secret must be valid JSON"))
		return
	}
	if err := os.WriteFile(s.cfg.AuthfilePath, []byte(content), 0o644); err != nil {
		log.Printf("Error saving pull secret: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to save pull secret"))
		return
	}
	s.mu.Lock()
	s.pullSecretPath, s.pullSecretDetected = s.cfg.AuthfilePath, true
	s.mu.Unlock()
	log.Printf("Pull secret saved to: %s", s.cfg.AuthfilePath)
	writeJSON(w, http.StatusOK, jsonObject{"message": "Pull secret saved successfully"})
}

func (s *Server) handlePullSecretDelete(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pullSecretPath != "" {
		if err := os.Remove(s.pullSecretPath); err != nil && !os.IsNotExist(err) {
			// The file may be a bind mount that cannot be removed; empty it instead.
			if err := os.WriteFile(s.pullSecretPath, nil, 0o644); err != nil {
				log.Printf("Error removing pull secret: %v", err)
				writeJSON(w, http.StatusInternalServerError, errorBody("Failed to remove pull secret"))
				return
			}
		}
	}
	s.pullSecretPath, s.pullSecretDetected = "", false
	log.Println("Pull secret removed")
	writeJSON(w, http.StatusOK, jsonObject{"message": "Pull secret removed successfully"})
}

// Hosts in a pull secret that are not container registries.
var nonRegistryHosts = map[string]bool{"cloud.openshift.com": true, "sso.redhat.com": true}

func (s *Server) handleRegistries(w http.ResponseWriter, _ *http.Request) {
	type registry struct {
		Registry string `json:"registry"`
		Username string `json:"username"`
		HasAuth  bool   `json:"hasAuth"`
		Status   string `json:"status"`
		Error    string `json:"error,omitempty"`
	}
	ps, hosts, err := s.readPullSecret()
	if err != nil {
		log.Printf("Error reading registries from pull secret: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to read registries"))
		return
	}
	registries := []registry{}
	if ps == nil {
		writeJSON(w, http.StatusOK, jsonObject{"registries": registries})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range hosts {
		auth, ok := ps.Auths[name]
		if !ok || nonRegistryHosts[name] {
			continue
		}
		username := ""
		if decoded, err := base64.StdEncoding.DecodeString(auth.Auth); err == nil {
			username, _, _ = strings.Cut(string(decoded), ":")
		}
		status := registryStatus{Status: "not_verified"}
		if cached, ok := s.registryCache[name]; ok {
			status = cached
		}
		registries = append(registries, registry{name, username, auth.Auth != "", status.Status, status.Error})
	}
	writeJSON(w, http.StatusOK, jsonObject{"registries": registries})
}

// orderedAuthHosts returns the keys of "auths" in file order, so the UI lists
// registries the same way the pull secret does.
func orderedAuthHosts(content []byte) []string {
	var top map[string]json.RawMessage
	if json.Unmarshal(content, &top) != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(top["auths"]))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil
	}
	var hosts []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return hosts
		}
		hosts = append(hosts, tok.(string))
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return hosts
		}
	}
	return hosts
}

var (
	realmRe   = regexp.MustCompile(`realm="([^"]+)"`)
	serviceRe = regexp.MustCompile(`service="([^"]+)"`)
)

func (s *Server) handleRegistryVerify(w http.ResponseWriter, r *http.Request) {
	_, body, ok := readBody(w, r)
	if !ok {
		return
	}
	registryName, _ := body["registry"].(string)
	if registryName == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("Registry is required"))
		return
	}

	respond := func(status, errMsg string, cache bool) {
		if cache {
			s.mu.Lock()
			s.registryCache[registryName] = registryStatus{status, errMsg}
			s.mu.Unlock()
		}
		resp := jsonObject{"registry": registryName, "status": status}
		if errMsg != "" {
			resp["error"] = errMsg
		}
		writeJSON(w, http.StatusOK, resp)
	}

	ps, _, err := s.readPullSecret()
	if err != nil {
		respond("failed", err.Error(), true)
		return
	}
	if ps == nil {
		respond("failed", "No pull secret configured", false)
		return
	}
	creds, found := ps.Auths[registryName]
	if !found || creds.Auth == "" {
		respond("failed", "No credentials found for this registry", false)
		return
	}

	get := func(target string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Basic "+creds.Auth)
		return s.client.Do(req)
	}

	resp, err := get("https://" + registryName + "/v2/")
	if err != nil {
		respond("failed", err.Error(), true)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		respond("authenticated", "", true)
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		wwwAuth := resp.Header.Get("Www-Authenticate")
		if m := realmRe.FindStringSubmatch(wwwAuth); m != nil {
			tokenURL, err := url.Parse(m[1])
			if err != nil {
				respond("failed", err.Error(), true)
				return
			}
			if sm := serviceRe.FindStringSubmatch(wwwAuth); sm != nil {
				q := tokenURL.Query()
				q.Set("service", sm[1])
				tokenURL.RawQuery = q.Encode()
			}
			tokenResp, err := get(tokenURL.String())
			if err != nil {
				respond("failed", err.Error(), true)
				return
			}
			defer tokenResp.Body.Close()
			if tokenResp.StatusCode >= 200 && tokenResp.StatusCode < 300 {
				respond("authenticated", "", true)
				return
			}
			snippet, _ := io.ReadAll(io.LimitReader(tokenResp.Body, 200))
			respond("failed", fmt.Sprintf("Authentication failed (%d): %s", tokenResp.StatusCode, snippet), true)
			return
		}
	}
	respond("failed", fmt.Sprintf("HTTP %d", resp.StatusCode), true)
}
