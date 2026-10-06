package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const validConfigYAML = `kind: ImageSetConfiguration
apiVersion: mirror.openshift.io/v2alpha1
mirror:
  platform:
    channels:
      - name: stable-4.21
        minVersion: "4.21.0"
        maxVersion: "4.21.4"
    graph: true
  operators: []
  additionalImages: []
`

const dummyPullSecret = `{"auths":{"registry.example.com":{"auth":"dXNlcjpwYXNz"},"cloud.openshift.com":{"auth":"eDp5"}}}`

type testEnv struct {
	t   *testing.T
	srv *Server
	cfg Config
}

// newTestEnv creates a server with temporary storage, the fixture catalog data
// and an app root that has no sync script.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	root := t.TempDir()
	storage := filepath.Join(root, "data")
	t.Setenv("STORAGE_DIR", storage)
	t.Setenv("OC_MIRROR_AUTHFILE", filepath.Join(storage, "pull-secret.json"))
	t.Setenv("OC_MIRROR_WORKDIR", root)
	t.Setenv("HOST_DATA_DIR", "")
	for _, key := range []string{"OC_MIRROR_CACHE_DIR", "OC_MIRROR_BASE_MIRROR_DIR", "OC_MIRROR_EPHEMERAL_DIR"} {
		t.Setenv(key, "")
	}
	cfg := ConfigFromEnv()
	fixture, err := filepath.Abs("../../tests/fixtures/catalog-data")
	if err != nil {
		t.Fatal(err)
	}
	cfg.BuiltinCatalogDir = fixture
	return &testEnv{t: t, srv: New(cfg), cfg: cfg}
}

type response struct {
	status  int
	header  http.Header
	body    []byte
	decoded any
}

func (r response) obj() map[string]any {
	m, _ := r.decoded.(map[string]any)
	return m
}

func (r response) arr() []any {
	a, _ := r.decoded.([]any)
	return a
}

func (e *testEnv) do(method, target string, body any) response {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	res := response{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
	_ = json.Unmarshal(res.body, &res.decoded)
	return res
}

func (e *testEnv) expectStatus(res response, want int) {
	e.t.Helper()
	if res.status != want {
		e.t.Fatalf("status = %d, want %d; body: %s", res.status, want, res.body)
	}
}

func errorText(res response) string {
	s, _ := res.obj()["error"].(string)
	return s
}

func containsAny(list []any, want string) bool {
	return slices.ContainsFunc(list, func(v any) bool { return v == want })
}

func TestHealth(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/health", nil)
	e.expectStatus(res, 200)
	body := res.obj()
	if body["status"] != "healthy" || body["service"] != "mirror-gui" {
		t.Errorf("body = %v", body)
	}
	ts, _ := body["timestamp"].(string)
	if parsed, err := time.Parse(time.RFC3339Nano, ts); err != nil || isoTime(parsed) != ts {
		t.Errorf("timestamp %q is not an ISO string", ts)
	}
}

func TestChannelsEndpoint(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/channels", nil)
	e.expectStatus(res, 200)
	for _, ch := range []string{"stable-4.16", "stable-4.17", "stable-4.18", "stable-4.19", "stable-4.20", "stable-4.21"} {
		if !containsAny(res.arr(), ch) {
			t.Errorf("missing %s", ch)
		}
	}
}

func TestCatalogs(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/catalogs", nil)
	e.expectStatus(res, 200)
	names := []any{}
	for _, c := range res.arr() {
		cat := c.(map[string]any)
		names = append(names, cat["name"])
		if count, _ := cat["operatorCount"].(float64); count < 1 {
			t.Errorf("operatorCount = %v", cat["operatorCount"])
		}
		for _, key := range []string{"url", "description", "digest", "syncedAt"} {
			if _, ok := cat[key]; !ok {
				t.Errorf("catalog missing %s", key)
			}
		}
	}
	for _, n := range []string{"redhat-operator-index", "certified-operator-index", "community-operator-index"} {
		if !containsAny(names, n) {
			t.Errorf("missing catalog %s", n)
		}
	}

	e.srv.failNextCatalogsGet = true
	res = e.do("GET", "/api/catalogs", nil)
	e.expectStatus(res, 500)
	if !strings.Contains(errorText(res), "Failed to get catalogs") {
		t.Errorf("error = %q", errorText(res))
	}
}

func TestOperators(t *testing.T) {
	e := newTestEnv(t)
	const catalog = "registry.redhat.io/redhat/redhat-operator-index:v4.21"

	res := e.do("GET", "/api/operators", nil)
	e.expectStatus(res, 200)
	if !containsAny(res.arr(), "advanced-cluster-management") || !containsAny(res.arr(), "openshift-pipelines-operator-rh") {
		t.Errorf("operators = %v", res.arr())
	}

	res = e.do("GET", "/api/operators?catalog="+url.QueryEscape(catalog), nil)
	e.expectStatus(res, 200)
	if !containsAny(res.arr(), "advanced-cluster-management") || !containsAny(res.arr(), "odf-operator") {
		t.Errorf("catalog operators = %v", res.arr())
	}

	res = e.do("GET", "/api/operators?detailed=true&catalog="+url.QueryEscape(catalog), nil)
	e.expectStatus(res, 200)
	var acm map[string]any
	for _, op := range res.arr() {
		if m := op.(map[string]any); m["name"] == "advanced-cluster-management" {
			acm = m
		}
	}
	if acm == nil || acm["defaultChannel"] != "release-2.16" {
		t.Fatalf("acm = %v", acm)
	}
	all, _ := acm["allChannels"].([]any)
	if !containsAny(all, "release-2.15") || !containsAny(all, "release-2.16") {
		t.Errorf("allChannels = %v", all)
	}

	e.srv.failNextOperatorsGet = true
	res = e.do("GET", "/api/operators", nil)
	e.expectStatus(res, 500)
	if !strings.Contains(errorText(res), "Failed to get operators") {
		t.Errorf("error = %q", errorText(res))
	}

	res = e.do("POST", "/api/operators/refresh-cache", nil)
	e.expectStatus(res, 200)
	if !strings.Contains(res.obj()["message"].(string), "refreshed") {
		t.Errorf("message = %v", res.obj()["message"])
	}
}

func TestOperatorVersions(t *testing.T) {
	e := newTestEnv(t)
	const catalog = "registry.redhat.io/redhat/redhat-operator-index:v4.21"

	res := e.do("GET", "/api/operators/advanced-cluster-management/versions?catalog="+url.QueryEscape(catalog), nil)
	e.expectStatus(res, 200)
	if versions, _ := res.obj()["versions"].([]any); !containsAny(versions, "2.16.0") {
		t.Errorf("versions = %v", versions)
	}

	res = e.do("GET", "/api/operators/advanced-cluster-management/versions?channel=release-2.15&catalog="+url.QueryEscape(catalog), nil)
	e.expectStatus(res, 200)
	versions, _ := res.obj()["versions"].([]any)
	if !containsAny(versions, "2.15.0") || !containsAny(versions, "2.15.1") || containsAny(versions, "2.16.0") {
		t.Errorf("channel versions = %v", versions)
	}

	res = e.do("GET", "/api/operators/nonexistent-operator-xyz/versions", nil)
	e.expectStatus(res, 404)
	if !strings.Contains(errorText(res), "not found") {
		t.Errorf("error = %q", errorText(res))
	}
}

func TestOperatorChannels(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/operator-channels/advanced-cluster-management", nil)
	e.expectStatus(res, 200)
	body := res.obj()
	channels, _ := body["channels"].([]any)
	if body["name"] != "advanced-cluster-management" || body["defaultChannel"] == nil || len(channels) == 0 {
		t.Errorf("body = %v", body)
	}
	for _, ch := range channels {
		if name, _ := ch.(map[string]any)["name"].(string); name == "" {
			t.Errorf("channel without name: %v", ch)
		}
	}

	e.expectStatus(e.do("GET", "/api/operator-channels/nonexistent-operator-xyz", nil), 404)

	res = e.do("GET", "/api/operators/channels", nil)
	e.expectStatus(res, 400)
	if !strings.Contains(errorText(res), "required") {
		t.Errorf("error = %q", errorText(res))
	}
	e.expectStatus(e.do("GET", "/api/operators/channels?catalogUrl=x", nil), 400)
	e.expectStatus(e.do("GET", "/api/operators/channels?operatorName=x", nil), 400)

	res = e.do("GET", "/api/operators/channels?operatorName=advanced-cluster-management&catalogUrl="+
		url.QueryEscape("registry.redhat.io/redhat/redhat-operator-index:v4.21"), nil)
	e.expectStatus(res, 200)
	if len(res.arr()) != 2 {
		t.Errorf("channels = %v", res.arr())
	}
}

func TestOperatorDependencies(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/operators/odf-operator/dependencies?catalogUrl="+
		url.QueryEscape("registry.redhat.io/redhat/redhat-operator-index:v4.21"), nil)
	e.expectStatus(res, 200)
	body := res.obj()
	deps, _ := body["dependencies"].([]any)
	if body["operator"] != "odf-operator" || len(deps) == 0 {
		t.Fatalf("body = %v", body)
	}
	names := []any{}
	for _, d := range deps {
		names = append(names, d.(map[string]any)["packageName"])
	}
	if !containsAny(names, "mcg-operator") {
		t.Errorf("dependencies = %v", names)
	}

	res = e.do("GET", "/api/operators/odf-operator/dependencies", nil)
	e.expectStatus(res, 200)
	if res.obj()["catalogType"] != "redhat-operator-index" {
		t.Errorf("catalogType = %v", res.obj()["catalogType"])
	}

	res = e.do("GET", "/api/operators/nonexistent-operator-xyz/dependencies", nil)
	e.expectStatus(res, 200)
	if deps, ok := res.obj()["dependencies"].([]any); !ok || len(deps) != 0 {
		t.Errorf("dependencies = %v", res.obj()["dependencies"])
	}
	if !strings.Contains(res.obj()["message"].(string), "No dependencies") {
		t.Errorf("message = %v", res.obj()["message"])
	}
}

func TestConfigSaveAndValidation(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/config/list", nil)
	e.expectStatus(res, 200)
	if res.arr() == nil {
		t.Errorf("expected array, got %s", res.body)
	}

	res = e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML, "name": "test-save.yaml"})
	e.expectStatus(res, 200)
	if res.obj()["filename"] != "test-save.yaml" || !strings.Contains(res.obj()["message"].(string), "successfully") {
		t.Errorf("body = %v", res.obj())
	}

	configObject := map[string]any{
		"kind": "ImageSetConfiguration", "apiVersion": "mirror.openshift.io/v2alpha1",
		"mirror": map[string]any{"platform": map[string]any{"channels": []any{map[string]any{"name": "stable-4.16"}}}},
	}
	res = e.do("POST", "/api/config/save", map[string]any{"config": configObject, "name": "test-config"})
	e.expectStatus(res, 200)
	if res.obj()["filename"] != "test-config.yaml" {
		t.Errorf("filename = %v", res.obj()["filename"])
	}
	saved, err := os.ReadFile(filepath.Join(e.cfg.ConfigsDir, "test-config.yaml"))
	if err != nil || !strings.Contains(string(saved), "kind: ImageSetConfiguration") {
		t.Errorf("saved object config = %q, %v", saved, err)
	}

	res = e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML, "name": "no-ext"})
	e.expectStatus(res, 200)
	if res.obj()["filename"] != "no-ext.yaml" {
		t.Errorf("filename = %v", res.obj()["filename"])
	}

	res = e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML})
	e.expectStatus(res, 200)
	if name, _ := res.obj()["filename"].(string); !strings.HasPrefix(name, "imageset-config-") {
		t.Errorf("generated filename = %q", name)
	}

	cases := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"name": "missing-config.yaml"}, "config is required"},
		{map[string]any{"name": "bad-kind.yaml", "config": map[string]any{"kind": "Other", "apiVersion": "mirror.openshift.io/v2alpha1", "mirror": map[string]any{}}}, "ImageSetConfiguration"},
		{map[string]any{"name": "bad-yaml.yaml", "config": "kind: ImageSetConfiguration\n  invalid: yaml: ["}, "Invalid YAML"},
		{map[string]any{"name": "bad-type.yaml", "config": 123}, "YAML string or JSON object"},
		{map[string]any{"name": 123, "config": validConfigYAML}, "name must be a string"},
		{map[string]any{"name": "../../evil", "config": validConfigYAML}, "Invalid filename"},
	}
	for _, c := range cases {
		res := e.do("POST", "/api/config/save", c.body)
		e.expectStatus(res, 400)
		if !strings.Contains(errorText(res), c.want) {
			t.Errorf("body %v: error = %q, want %q", c.body, errorText(res), c.want)
		}
	}

	res = e.do("GET", "/api/config/list", nil)
	e.expectStatus(res, 200)
	if len(res.arr()) != 4 {
		t.Errorf("config list = %s", res.body)
	}
	first := res.arr()[0].(map[string]any)
	if size, _ := first["size"].(string); !strings.HasSuffix(size, " KB") {
		t.Errorf("size = %v", first["size"])
	}
}

func TestConfigUpload(t *testing.T) {
	e := newTestEnv(t)
	cases := []struct {
		content, want string
	}{
		{"apiVersion: mirror.openshift.io/v2alpha1\nmirror: {}", "ImageSetConfiguration"},
		{"kind: ImageSetConfiguration\napiVersion: v1\nmirror: {}", "mirror.openshift.io"},
		{"kind: ImageSetConfiguration\napiVersion: mirror.openshift.io/v2alpha1\n", "mirror"},
		{"kind: ImageSetConfiguration\n  invalid: yaml: [", "Invalid YAML"},
	}
	for _, c := range cases {
		res := e.do("POST", "/api/config/upload", map[string]any{"filename": "bad.yaml", "content": c.content})
		e.expectStatus(res, 400)
		if !strings.Contains(errorText(res), c.want) {
			t.Errorf("content %q: error = %q, want %q", c.content, errorText(res), c.want)
		}
	}

	e.expectStatus(e.do("POST", "/api/config/upload", map[string]any{"filename": "x.yaml"}), 400)
	e.expectStatus(e.do("POST", "/api/config/upload", map[string]any{"filename": "../x.yaml", "content": validConfigYAML}), 400)

	res := e.do("POST", "/api/config/upload", map[string]any{"filename": "valid-upload", "content": validConfigYAML})
	e.expectStatus(res, 200)
	if res.obj()["filename"] != "valid-upload.yaml" {
		t.Errorf("filename = %v", res.obj()["filename"])
	}

	res = e.do("POST", "/api/config/upload", map[string]any{"filename": "valid-upload.yaml", "content": validConfigYAML})
	e.expectStatus(res, 409)
	if !strings.Contains(errorText(res), "already exists") {
		t.Errorf("error = %q", errorText(res))
	}
}

func TestConfigDownloadAndDelete(t *testing.T) {
	e := newTestEnv(t)
	e.expectStatus(e.do("GET", "/api/config/download/config.txt", nil), 400)
	res := e.do("GET", "/api/config/download/"+url.PathEscape("../passwd"), nil)
	e.expectStatus(res, 400)
	if !strings.Contains(strings.ToLower(errorText(res)), "invalid filename") {
		t.Errorf("error = %q", errorText(res))
	}
	e.expectStatus(e.do("GET", "/api/config/download/does-not-exist.yaml", nil), 404)

	const name = "download-test-config.yaml"
	e.expectStatus(e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML, "name": name}), 200)
	res = e.do("GET", "/api/config/download/"+name, nil)
	e.expectStatus(res, 200)
	if !strings.Contains(res.header.Get("Content-Type"), "yaml") ||
		!strings.Contains(res.header.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(res.header.Get("Content-Disposition"), name) ||
		!strings.Contains(string(res.body), "ImageSetConfiguration") {
		t.Errorf("download headers %v body %s", res.header, res.body)
	}

	e.expectStatus(e.do("DELETE", "/api/config/delete/nonexistent-file.yaml", nil), 404)
	e.expectStatus(e.do("DELETE", "/api/config/delete/evil%2E%2E%2F%2E%2E%2Fetc", nil), 400)
	e.expectStatus(e.do("DELETE", "/api/config/delete/"+name, nil), 200)
	e.expectStatus(e.do("GET", "/api/config/download/"+name, nil), 404)
}

func TestMirrorFolders(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/mirror-folders", nil)
	e.expectStatus(res, 200)
	if folders, ok := res.obj()["folders"].([]any); !ok || !containsAny(folders, "default") {
		t.Errorf("folders = %v", res.obj()["folders"])
	}

	res = e.do("POST", "/api/mirror-folders", map[string]any{"name": "bad name!"})
	e.expectStatus(res, 400)
	if !strings.Contains(errorText(res), "letters, numbers, dashes") {
		t.Errorf("error = %q", errorText(res))
	}
	e.expectStatus(e.do("POST", "/api/mirror-folders", map[string]any{"name": "../x"}), 400)
	e.expectStatus(e.do("POST", "/api/mirror-folders", map[string]any{}), 400)

	res = e.do("POST", "/api/mirror-folders", map[string]any{"name": "itest_folder"})
	e.expectStatus(res, 200)
	if res.obj()["created"] != "itest_folder" {
		t.Errorf("created = %v", res.obj()["created"])
	}
	res = e.do("GET", "/api/mirror-folders", nil)
	if folders, _ := res.obj()["folders"].([]any); !containsAny(folders, "itest_folder") {
		t.Errorf("folders = %v", folders)
	}
}

func TestPullSecret(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/pull-secret/status", nil)
	e.expectStatus(res, 200)
	if res.obj()["detected"] != false || res.obj()["path"] != nil {
		t.Errorf("initial status = %v", res.obj())
	}

	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{"content": ""}), 400)
	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{}), 400)
	res = e.do("POST", "/api/pull-secret", map[string]any{"content": "not-json-content"})
	e.expectStatus(res, 400)
	if !strings.Contains(errorText(res), "valid JSON") {
		t.Errorf("error = %q", errorText(res))
	}

	res = e.do("POST", "/api/pull-secret", map[string]any{"content": dummyPullSecret})
	e.expectStatus(res, 200)
	if !strings.Contains(res.obj()["message"].(string), "saved") {
		t.Errorf("message = %v", res.obj()["message"])
	}
	res = e.do("GET", "/api/pull-secret/status", nil)
	if res.obj()["detected"] != true || res.obj()["path"] != e.cfg.AuthfilePath {
		t.Errorf("status after save = %v", res.obj())
	}
	res = e.do("GET", "/api/pull-secret/content", nil)
	if res.obj()["content"] != dummyPullSecret {
		t.Errorf("content = %v", res.obj()["content"])
	}

	res = e.do("GET", "/api/registries", nil)
	e.expectStatus(res, 200)
	registries, _ := res.obj()["registries"].([]any)
	if len(registries) != 1 {
		t.Fatalf("registries = %v", registries)
	}
	reg := registries[0].(map[string]any)
	if reg["registry"] != "registry.example.com" || reg["username"] != "user" || reg["hasAuth"] != true || reg["status"] != "not_verified" {
		t.Errorf("registry = %v", reg)
	}

	e.expectStatus(e.do("DELETE", "/api/pull-secret", nil), 200)
	res = e.do("GET", "/api/pull-secret/status", nil)
	if res.obj()["detected"] != false || res.obj()["path"] != nil {
		t.Errorf("status after delete = %v", res.obj())
	}
	if res := e.do("GET", "/api/pull-secret/content", nil); res.obj()["content"] != "" {
		t.Errorf("content after delete = %v", res.obj()["content"])
	}
	if _, err := os.Stat(e.cfg.AuthfilePath); !os.IsNotExist(err) {
		t.Errorf("pull secret file still exists: %v", err)
	}
	if res := e.do("GET", "/api/registries", nil); len(res.obj()["registries"].([]any)) != 0 {
		t.Errorf("registries after delete = %v", res.obj())
	}
}

func TestRegistryVerify(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("POST", "/api/registries/verify", map[string]any{})
	e.expectStatus(res, 400)
	if !strings.Contains(strings.ToLower(errorText(res)), "registry is required") {
		t.Errorf("error = %q", errorText(res))
	}

	res = e.do("POST", "/api/registries/verify", map[string]any{"registry": "registry.example.com"})
	e.expectStatus(res, 200)
	if res.obj()["status"] != "failed" || res.obj()["error"] != "No pull secret configured" {
		t.Errorf("body = %v", res.obj())
	}

	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{"content": dummyPullSecret}), 200)
	res = e.do("POST", "/api/registries/verify", map[string]any{"registry": "no-such-registry.invalid.test"})
	e.expectStatus(res, 200)
	if res.obj()["registry"] != "no-such-registry.invalid.test" || res.obj()["status"] != "failed" ||
		!strings.Contains(strings.ToLower(errorText(res)), "no credentials found") {
		t.Errorf("body = %v", res.obj())
	}
}

func TestRegistryVerifyTokenFlow(t *testing.T) {
	e := newTestEnv(t)
	var tokenAuth, tokenService string
	registry := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.Header().Set("Www-Authenticate", `Bearer realm="https://`+r.Host+`/token",service="test-registry"`)
			w.WriteHeader(http.StatusUnauthorized)
		case "/token":
			tokenAuth, tokenService = r.Header.Get("Authorization"), r.URL.Query().Get("service")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer registry.Close()
	e.srv.client = registry.Client()

	host := strings.TrimPrefix(registry.URL, "https://")
	secret := `{"auths":{"` + host + `":{"auth":"dXNlcjpwYXNz"}}}`
	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{"content": secret}), 200)

	res := e.do("POST", "/api/registries/verify", map[string]any{"registry": host})
	e.expectStatus(res, 200)
	if res.obj()["status"] != "authenticated" {
		t.Errorf("body = %v", res.obj())
	}
	if tokenAuth != "Basic dXNlcjpwYXNz" || tokenService != "test-registry" {
		t.Errorf("token request auth=%q service=%q", tokenAuth, tokenService)
	}
	res = e.do("GET", "/api/registries", nil)
	if reg := res.obj()["registries"].([]any)[0].(map[string]any); reg["status"] != "authenticated" {
		t.Errorf("cached status = %v", reg)
	}
}

func TestSystemEndpoints(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/system/paths", nil)
	e.expectStatus(res, 200)
	paths, _ := res.obj()["paths"].([]any)
	if len(paths) != 4 {
		t.Fatalf("paths = %v", paths)
	}
	for _, p := range paths {
		m := p.(map[string]any)
		if _, ok := m["available"].(bool); !ok || m["path"] == nil || m["label"] == nil || m["description"] == nil {
			t.Errorf("path entry = %v", m)
		}
	}

	res = e.do("GET", "/api/system/info", nil)
	e.expectStatus(res, 200)
	info := res.obj()
	for _, key := range []string{"availableDiskSpace", "totalDiskSpace", "cacheSizeBytes"} {
		if _, ok := info[key].(float64); !ok {
			t.Errorf("%s = %v", key, info[key])
		}
	}
	if hostDir, _ := info["hostDataDir"].(string); hostDir == "" || info["ocMirrorVersion"] == nil || info["systemArchitecture"] == nil {
		t.Errorf("info = %v", info)
	}

	res = e.do("GET", "/api/system/status", nil)
	e.expectStatus(res, 200)
	if !slices.Contains([]any{"healthy", "degraded", "warning", "error"}, res.obj()["systemHealth"]) {
		t.Errorf("systemHealth = %v", res.obj()["systemHealth"])
	}
	if _, ok := res.obj()["pullSecretDetected"].(bool); !ok {
		t.Errorf("pullSecretDetected = %v", res.obj()["pullSecretDetected"])
	}
}

func TestHostCacheDirMapping(t *testing.T) {
	e := newTestEnv(t)
	e.srv.cfg.HostDataDir = "/host/data"
	res := e.do("GET", "/api/system/info", nil)
	if res.obj()["hostDataDir"] != "/host/data" || res.obj()["hostCacheDir"] != "/host/data/cache" {
		t.Errorf("info = %v", res.obj())
	}
}

func TestCacheCleanup(t *testing.T) {
	e := newTestEnv(t)
	if err := os.MkdirAll(filepath.Join(e.cfg.CacheDir, "blobs", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	res := e.do("POST", "/api/cache/cleanup", nil)
	e.expectStatus(res, 200)
	if entries, _ := os.ReadDir(e.cfg.CacheDir); len(entries) != 0 {
		t.Errorf("cache not empty: %v", entries)
	}
}

func seedOperation(t *testing.T, e *testEnv, id string, op map[string]any) {
	t.Helper()
	b, _ := json.MarshalIndent(op, "", "  ")
	if err := os.WriteFile(filepath.Join(e.cfg.OperationsDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOperationsListAndStats(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/operations", nil)
	e.expectStatus(res, 200)
	if string(bytes.TrimSpace(res.body)) != "[]" {
		t.Errorf("initial operations = %s", res.body)
	}

	older := time.Now().Add(-time.Hour)
	seedOperation(t, e, "op-old", map[string]any{"id": "op-old", "configFile": "a.yaml", "status": "failed", "startedAt": isoTime(older), "logs": []string{}})
	seedOperation(t, e, "op-new", map[string]any{"id": "op-new", "configFile": "b.yaml", "status": "success", "startedAt": isoTime(time.Now()), "logs": []string{}, "extra": "kept"})
	if err := os.WriteFile(filepath.Join(e.cfg.OperationsDir, "corrupt.json"), []byte(`{"id":"corrupt","name":"Trun`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/operations", "/api/operations/history", "/api/operations/recent"} {
		res = e.do("GET", path, nil)
		e.expectStatus(res, 200)
		ops := res.arr()
		if len(ops) != 2 || ops[0].(map[string]any)["id"] != "op-new" || ops[0].(map[string]any)["extra"] != "kept" {
			t.Errorf("%s = %s", path, res.body)
		}
	}

	res = e.do("GET", "/api/stats", nil)
	e.expectStatus(res, 200)
	stats := res.obj()
	if stats["totalOperations"] != 2.0 || stats["successfulOperations"] != 1.0 || stats["failedOperations"] != 1.0 || stats["runningOperations"] != 0.0 {
		t.Errorf("stats = %v", stats)
	}
}

func TestOperationStopWithoutProcessAndDelete(t *testing.T) {
	e := newTestEnv(t)
	seedOperation(t, e, "test-roundtrip-op", map[string]any{
		"id": "test-roundtrip-op", "name": "RT Op", "configFile": "rt.yaml", "status": "running",
		"startedAt": isoTime(time.Now()), "logs": []string{},
	})
	res := e.do("POST", "/api/operations/test-roundtrip-op/stop", nil)
	e.expectStatus(res, 200)
	if !strings.Contains(res.obj()["message"].(string), "stopped") {
		t.Errorf("message = %v", res.obj()["message"])
	}
	op := e.do("GET", "/api/operations", nil).arr()[0].(map[string]any)
	if op["status"] != "stopped" || op["configFile"] != "rt.yaml" || op["name"] != "RT Op" {
		t.Errorf("operation = %v", op)
	}
	if v, ok := op["errorMessage"]; !ok || v != nil {
		t.Errorf("errorMessage = %v (present %v)", v, ok)
	}

	e.expectStatus(e.do("POST", "/api/operations/missing-op/stop", nil), 200)
	e.expectStatus(e.do("POST", "/api/operations/..%2Fx/stop", nil), 400)

	res = e.do("DELETE", "/api/operations/test-roundtrip-op", nil)
	e.expectStatus(res, 200)
	if len(e.do("GET", "/api/operations", nil).arr()) != 0 {
		t.Error("operation not deleted")
	}
	e.expectStatus(e.do("DELETE", "/api/operations/00000000-0000-0000-0000-000000000000", nil), 200)
	e.expectStatus(e.do("DELETE", "/api/operations/..%2F..%2Fconfigs%2Fx", nil), 400)
}

func TestOperationLogsAndDetails(t *testing.T) {
	e := newTestEnv(t)
	const id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	logs := []string{
		"line 1", "📌 images to copy 10", "✓ 3 / 3 operator images mirrored successfully",
		"🔍 collecting release images", "Success copying quay.io/release:4.21 ➡️ cache",
	}
	seedOperation(t, e, id, map[string]any{
		"id": id, "configFile": "lifecycle.yaml", "status": "success", "startedAt": isoTime(time.Now()), "logs": logs,
	})

	res := e.do("GET", "/api/operations/"+id+"/logs", nil)
	e.expectStatus(res, 200)
	if res.obj()["logs"] != strings.Join(logs, "\n") {
		t.Errorf("logs from record = %v", res.obj()["logs"])
	}
	if err := os.WriteFile(filepath.Join(e.cfg.LogsDir, id+".log"), []byte("from file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := e.do("GET", "/api/operations/"+id+"/logs", nil); res.obj()["logs"] != "from file" {
		t.Errorf("logs from file = %v", res.obj()["logs"])
	}

	res = e.do("GET", "/api/operations/"+id+"/details", nil)
	e.expectStatus(res, 200)
	d := res.obj()
	if d["imagesMirrored"] != 10.0 || d["operatorsMirrored"] != 3.0 || d["totalSize"] != float64(10*50*1024*1024) ||
		d["platformImages"] != 1.0 || d["configFile"] != "lifecycle.yaml" || len(d["manifestFiles"].([]any)) != 3 {
		t.Errorf("details = %v", d)
	}

	e.expectStatus(e.do("GET", "/api/operations/00000000-0000-0000-0000-000000000000/details", nil), 404)
}

func TestOperationStartValidation(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("POST", "/api/operations/start", map[string]any{"configFile": "nonexistent.yaml"})
	e.expectStatus(res, 404)
	if !strings.Contains(errorText(res), "not found") {
		t.Errorf("error = %q", errorText(res))
	}
	e.expectStatus(e.do("POST", "/api/operations/start", map[string]any{}), 400)
	e.expectStatus(e.do("POST", "/api/operations/start", map[string]any{"configFile": "../configs/x.yaml"}), 400)

	e.expectStatus(e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML, "name": "ops-test-config.yaml"}), 200)
	cases := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"mirrorDestinationSubdir": "../evil"}, "path separators"},
		{map[string]any{"mirrorDestinationSubdir": "bad@name!"}, "invalid characters"},
		{map[string]any{"optionalFlags": map[string]any{"unknownFlag": true}}, "Unknown optional flag"},
		{map[string]any{"optionalFlags": map[string]any{"imageTimeout": "10minutes"}}, "imageTimeout"},
		{map[string]any{"optionalFlags": map[string]any{"imageTimeout": "0s"}}, "greater than 0"},
		{map[string]any{"optionalFlags": map[string]any{"retryTimes": 1.5}}, "retryTimes"},
		{map[string]any{"optionalFlags": map[string]any{"retryTimes": float64(1<<53 + 2)}}, "retryTimes"},
	}
	for _, c := range cases {
		c.body["configFile"] = "ops-test-config.yaml"
		res := e.do("POST", "/api/operations/start", c.body)
		e.expectStatus(res, 400)
		if !strings.Contains(errorText(res), c.want) {
			t.Errorf("body %v: error = %q, want %q", c.body, errorText(res), c.want)
		}
	}
}

// installFakeOcMirror puts a fake oc-mirror script first on PATH.
func installFakeOcMirror(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "oc-mirror"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (e *testEnv) startOperation(body map[string]any) string {
	e.t.Helper()
	e.expectStatus(e.do("POST", "/api/config/save", map[string]any{"config": validConfigYAML, "name": "run.yaml"}), 200)
	body["configFile"] = "run.yaml"
	res := e.do("POST", "/api/operations/start", body)
	e.expectStatus(res, 200)
	id, _ := res.obj()["operationId"].(string)
	if id == "" || !strings.Contains(res.obj()["message"].(string), "success") {
		e.t.Fatalf("start response = %v", res.obj())
	}
	return id
}

func (e *testEnv) waitForStatus(id string, statuses ...string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		op, err := e.srv.getOperation(id)
		if err == nil && slices.Contains(statuses, op["status"].(string)) {
			return op
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("operation %s never reached %v", id, statuses)
	return nil
}

func TestOperationRunSuccessPassesFlags(t *testing.T) {
	e := newTestEnv(t)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("OC_MIRROR_ARGS_FILE", argsFile)
	installFakeOcMirror(t, `printf "%s\n" "$@" > "$OC_MIRROR_ARGS_FILE"; echo "📌 images to copy 2"`)

	id := e.startOperation(map[string]any{
		"mirrorDestinationSubdir": "odf",
		"optionalFlags":           map[string]any{"removeSignatures": true, "imageTimeout": "10m", "retryDelay": "30s", "retryTimes": 3},
	})
	op := e.waitForStatus(id, "success", "failed")
	if op["status"] != "success" || op["mirrorDestination"] != filepath.Join(e.cfg.MirrorBaseDir, "odf") {
		t.Errorf("operation = %v", op)
	}
	if _, ok := op["duration"].(float64); !ok || op["completedAt"] == nil {
		t.Errorf("missing completion fields: %v", op)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Fields(string(raw))
	mirrorURL := args[len(args)-1]
	if mirrorURL != "file://"+filepath.Join(e.cfg.MirrorBaseDir, "odf") {
		t.Errorf("mirror URL = %q", mirrorURL)
	}
	for _, want := range []string{"--v2", "--remove-signatures", "--image-timeout", "10m", "--retry-delay", "30s", "--retry-times", "3"} {
		if !slices.Contains(args[:len(args)-1], want) {
			t.Errorf("args %v missing %q before mirror URL", args, want)
		}
	}

	logFile, _ := os.ReadFile(filepath.Join(e.cfg.LogsDir, id+".log"))
	if !strings.Contains(string(logFile), "images to copy 2") {
		t.Errorf("log file = %q", logFile)
	}
}

func TestOperationRunFailureExtractsError(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo "2026/01/02 03:04:05  [ERROR]  : unable to reach registry" >&2; exit 1`)
	id := e.startOperation(map[string]any{})
	op := e.waitForStatus(id, "success", "failed")
	if op["status"] != "failed" || op["errorMessage"] != "unable to reach registry" {
		t.Errorf("operation = %v", op)
	}
}

func TestOperationStopRunningProcess(t *testing.T) {
	e := newTestEnv(t)
	installFakeOcMirror(t, `echo started; exec sleep 30`)
	id := e.startOperation(map[string]any{})
	time.Sleep(100 * time.Millisecond)

	e.expectStatus(e.do("POST", "/api/operations/"+id+"/stop", nil), 200)
	op := e.waitForStatus(id, "stopped", "failed", "success")
	if op["status"] != "stopped" {
		t.Errorf("operation = %v", op)
	}
}

func TestOperationLogStream(t *testing.T) {
	e := newTestEnv(t)
	orig := logStreamInterval
	logStreamInterval = 10 * time.Millisecond
	t.Cleanup(func() { logStreamInterval = orig })

	const id = "stream-op"
	seedOperation(t, e, id, map[string]any{"id": id, "status": "success", "startedAt": isoTime(time.Now()), "logs": []string{}})
	if err := os.WriteFile(filepath.Join(e.cfg.LogsDir, id+".log"), []byte("line 1\nline 2"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(e.srv)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/operations/" + id + "/logstream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("content type = %q", resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	want := "data: line 1\ndata: line 2\n\nevent: done\ndata: {\"id\":\"stream-op\",\"status\":\"success\"}\n\n"
	if string(body) != want {
		t.Errorf("stream = %q, want %q", body, want)
	}

	resp, err = http.Get(ts.URL + "/api/operations/no-log-op/logstream")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"status":"unknown"`) {
		t.Errorf("missing-log stream = %q", body)
	}
}

func TestCatalogSyncEndpoints(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/api/catalogs/sync/status", nil)
	e.expectStatus(res, 200)
	if res.obj()["status"] != "idle" || res.obj()["hasRuntimeSyncData"] != false {
		t.Errorf("status = %v", res.obj())
	}
	if _, ok := res.obj()["logs"].([]any); !ok {
		t.Errorf("logs = %v", res.obj()["logs"])
	}

	res = e.do("DELETE", "/api/catalogs/sync/data", nil)
	e.expectStatus(res, 200)
	if !strings.Contains(strings.ToLower(res.obj()["message"].(string)), "no synced catalog data to clear") {
		t.Errorf("message = %v", res.obj()["message"])
	}

	e.expectStatus(e.do("POST", "/api/catalogs/sync", nil), 400)
	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{"content": dummyPullSecret}), 200)
	t.Setenv("PATH", t.TempDir())
	res = e.do("POST", "/api/catalogs/sync", nil)
	e.expectStatus(res, 500)
	if !strings.Contains(errorText(res), "oc CLI is missing") {
		t.Errorf("error = %q", errorText(res))
	}
}

// installFakeOC puts a fake `oc` first on PATH whose `image extract` copies the
// catalogmeta test snapshot and whose `image info` prints a digest.
func installFakeOC(t *testing.T) {
	t.Helper()
	configs, err := filepath.Abs("../catalogmeta/testdata/snapshot/configs")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := `#!/bin/sh
dest=""
while [ $# -gt 0 ]; do
  [ "$1" = "--path" ] && { dest="${2#/configs/:}"; shift; }
  shift
done
if [ -n "$dest" ]; then cp -r "` + configs + `"/. "$dest"/; else echo '{"digest": "sha256:fake"}'; fi
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCatalogSyncRun(t *testing.T) {
	e := newTestEnv(t)
	installFakeOC(t)
	e.srv.syncVersions = []string{"4.21"}
	e.srv.syncCatalogTypes = []string{"redhat-operator-index"}
	e.srv.syncRetryDelay = 0
	e.expectStatus(e.do("POST", "/api/pull-secret", map[string]any{"content": dummyPullSecret}), 200)
	// Warm the cache so the sync can report a diff against the fixture data.
	e.expectStatus(e.do("GET", "/api/catalogs", nil), 200)

	res := e.do("POST", "/api/catalogs/sync", nil)
	e.expectStatus(res, 200)

	var status map[string]any
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status = e.do("GET", "/api/catalogs/sync/status", nil).obj()
		if status["status"] != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status["status"] != "completed" || status["successCount"] != 1.0 || status["failedCount"] != 0.0 ||
		status["totalCount"] != 1.0 || status["completedCatalogs"] != 1.0 ||
		status["currentCatalog"] != "redhat-operator-index v4.21" || status["hasRuntimeSyncData"] != true {
		t.Fatalf("status = %v", status)
	}
	logs, _ := status["logs"].([]any)
	if !containsAny(logs, "Completed: 1/1 catalogs successful, 0 failed") {
		t.Errorf("logs = %v", logs)
	}
	// Only one catalog was synced, so the other fixture catalogs show up as removed.
	diff, _ := status["diff"].([]any)
	if len(diff) != 3 {
		t.Fatalf("diff = %v", status["diff"])
	}
	first := diff[0].(map[string]any)
	if first["catalog"] != "redhat-operator-index:v4.21" ||
		!containsAny(first["newOperators"].([]any), "standard-operator") ||
		!containsAny(first["removedOperators"].([]any), "advanced-cluster-management") {
		t.Errorf("diff = %v", first)
	}

	const catalog = "registry.redhat.io/redhat/redhat-operator-index:v4.21"
	res = e.do("GET", "/api/operators?catalog="+url.QueryEscape(catalog), nil)
	if !containsAny(res.arr(), "standard-operator") {
		t.Errorf("operators after sync = %v", res.arr())
	}
	res = e.do("GET", "/api/operators/standard-operator/dependencies?catalogUrl="+url.QueryEscape(catalog), nil)
	if deps, _ := res.obj()["dependencies"].([]any); len(deps) != 2 {
		t.Errorf("dependencies after sync = %v", res.obj())
	}
	res = e.do("GET", "/api/catalogs", nil)
	if cat := res.arr()[0].(map[string]any); cat["digest"] != "sha256:fake" || cat["syncedAt"] == nil {
		t.Errorf("catalog after sync = %v", cat)
	}

	res = e.do("DELETE", "/api/catalogs/sync/data", nil)
	e.expectStatus(res, 200)
	if !strings.Contains(res.obj()["message"].(string), "Falling back") {
		t.Errorf("message = %v", res.obj()["message"])
	}
	res = e.do("GET", "/api/operators?catalog="+url.QueryEscape(catalog), nil)
	if containsAny(res.arr(), "standard-operator") || !containsAny(res.arr(), "odf-operator") {
		t.Errorf("operators after clearing sync data = %v", res.arr())
	}
}

func TestFrontendServing(t *testing.T) {
	e := newTestEnv(t)
	res := e.do("GET", "/", nil)
	e.expectStatus(res, 404)

	if err := os.MkdirAll(filepath.Join(e.cfg.DistDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.cfg.DistDir, "index.html"), []byte("<html>app</html>"), 0o644)
	os.WriteFile(filepath.Join(e.cfg.DistDir, "assets", "app.js"), []byte("console.log(1)"), 0o644)

	for _, path := range []string{"/", "/settings", "/history/123"} {
		res = e.do("GET", path, nil)
		e.expectStatus(res, 200)
		if string(res.body) != "<html>app</html>" {
			t.Errorf("%s = %q", path, res.body)
		}
	}
	res = e.do("GET", "/assets/app.js", nil)
	e.expectStatus(res, 200)
	if string(res.body) != "console.log(1)" || !strings.Contains(res.header.Get("Cache-Control"), "max-age") {
		t.Errorf("asset = %q headers %v", res.body, res.header)
	}

	res = e.do("GET", "/api/does-not-exist", nil)
	e.expectStatus(res, 404)
	if errorText(res) != "Not found" {
		t.Errorf("unknown api route = %s", res.body)
	}
}

func TestCORSAndBadJSON(t *testing.T) {
	e := newTestEnv(t)
	req := httptest.NewRequest("OPTIONS", "/api/config/save", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" ||
		rec.Header().Get("Access-Control-Allow-Headers") != "content-type" {
		t.Errorf("preflight = %d %v", rec.Code, rec.Header())
	}

	req = httptest.NewRequest("POST", "/api/config/save", strings.NewReader("{not json"))
	rec = httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON status = %d", rec.Code)
	}
}
