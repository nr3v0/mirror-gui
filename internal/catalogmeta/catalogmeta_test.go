package catalogmeta

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The expected-*.json golden files were produced by the former Python
// implementation (scripts/catalog_metadata.py) from testdata/snapshot.
func TestGenerateSnapshotMatchesGolden(t *testing.T) {
	operators, dependencies, warnings := GenerateSnapshot("testdata/snapshot", "certified-operator-index", "v4.21")

	assertJSONEqual(t, operators, "testdata/expected-operators.json")
	assertJSONEqual(t, dependencies, "testdata/expected-dependencies.json")

	if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "catalog.json: ") {
		t.Errorf("warnings = %q, want one for broken/catalog.json", warnings)
	}
}

func assertJSONEqual(t *testing.T, got any, goldenPath string) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if err := json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		pretty, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("output differs from %s; got:\n%s", goldenPath, pretty)
	}
}

func TestGenerateSnapshotMissingConfigs(t *testing.T) {
	operators, dependencies, warnings := GenerateSnapshot(t.TempDir(), "x", "v1")
	if len(operators) != 0 || len(dependencies) != 0 || len(warnings) != 1 || !strings.HasPrefix(warnings[0], "Missing configs directory") {
		t.Errorf("operators=%v dependencies=%v warnings=%v", operators, dependencies, warnings)
	}
	if b, _ := json.Marshal(operators); string(b) != "[]" {
		t.Errorf("empty operators marshal as %s, want []", b)
	}
}

func TestCompareVersions(t *testing.T) {
	ordered := []string{"0.9.0", "1.0.0", "1.0.0-rc1", "1.0.0-2", "1.0.0-10", "1.2.0", "1.10.0", "99999999999999999999.0.0"}
	for i := 0; i < len(ordered)-1; i++ {
		if c := CompareVersions(ordered[i], ordered[i+1]); c >= 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want < 0", ordered[i], ordered[i+1], c)
		}
		if c := CompareVersions(ordered[i+1], ordered[i]); c <= 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want > 0", ordered[i+1], ordered[i], c)
		}
	}
	if CompareVersions("1.0.0", "1.0.0") != 0 || CompareVersions("latest", "stable") != 0 {
		t.Error("expected equal comparisons")
	}
	if got := SortVersions([]string{"1.10.0", "", "1.2.0", "1.10.0", "1.0.0-2", "1.0.0"}); !reflect.DeepEqual(got, []string{"1.0.0", "1.0.0-2", "1.2.0", "1.10.0"}) {
		t.Errorf("SortVersions = %v", got)
	}
}

func TestExtractVersionFromName(t *testing.T) {
	cases := map[string]string{
		"foo.v1.2.3":         "1.2.3",
		"foo.1.2.3-rc.1":     "1.2.3-rc.1",
		"v4.5.6":             "4.5.6",
		"7.8.9+build":        "7.8.9+build",
		"foo-operator.v1.2":  "",
		"no-version-in-here": "",
	}
	for name, want := range cases {
		if got := extractVersionFromName(name); got != want {
			t.Errorf("extractVersionFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestFinalizeSnapshotAndIndex(t *testing.T) {
	dataDir := t.TempDir()
	copyDir(t, "testdata/snapshot/configs", filepath.Join(SnapshotDir(dataDir, "certified-operator-index", "4.21"), "configs"))

	var lines []string
	count, err := FinalizeSnapshot(dataDir, "certified-operator-index", "4.21", "from-build", func(l string) { lines = append(lines, l) })
	if err != nil || count != 7 {
		t.Fatalf("FinalizeSnapshot = %d, %v", count, err)
	}
	snapshot := SnapshotDir(dataDir, "certified-operator-index", "4.21")
	if _, err := os.Stat(filepath.Join(snapshot, "configs")); !os.IsNotExist(err) {
		t.Error("configs directory was not removed")
	}
	if _, err := FinalizeSnapshot(dataDir, "redhat-operator-index", "4.21", "x", func(string) {}); err == nil {
		t.Error("expected error for snapshot without configs")
	}

	var info CatalogInfo
	readJSON(t, filepath.Join(snapshot, "catalog-info.json"), &info)
	if info.OperatorCount != 7 || info.Digest != "from-build" || info.OCPVersion != "v4.21" ||
		info.CatalogURL != "registry.redhat.io/redhat/certified-operator-index:v4.21" || len(info.SyncedAt) != len("2006-01-02T15:04:05Z") {
		t.Errorf("catalog-info = %+v", info)
	}

	if err := WriteIndex(dataDir, []string{"4.20", "4.21"}, []string{"redhat-operator-index", "certified-operator-index"}); err != nil {
		t.Fatal(err)
	}
	var index catalogIndex
	readJSON(t, filepath.Join(dataDir, "catalog-index.json"), &index)
	if !reflect.DeepEqual(index.OCPVersions, []string{"4.20", "4.21"}) || len(index.Catalogs) != 1 || index.Catalogs[0] != info {
		t.Errorf("index = %+v", index)
	}
}

// installFakeOC puts a fake `oc` on PATH. `oc image extract` copies the test
// snapshot configs (or fails for catalogs whose URL contains $FAKE_OC_FAIL);
// `oc image info` prints a digest. Every invocation is appended to $FAKE_OC_LOG.
func installFakeOC(t *testing.T, fail string) string {
	t.Helper()
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")
	configs, _ := filepath.Abs("testdata/snapshot/configs")
	script := `#!/bin/sh
echo "$@" >> "$FAKE_OC_LOG"
url=""; dest=""
while [ $# -gt 0 ]; do
  case "$1" in
    --path) dest="${2#/configs/:}"; shift ;;
    registry.*) url="$1" ;;
  esac
  shift
done
case "$url" in *"$FAKE_OC_FAIL"*) [ -n "$FAKE_OC_FAIL" ] && { echo "error: unauthorized" >&2; exit 1; } ;; esac
if [ -n "$dest" ]; then cp -r "$FAKE_OC_CONFIGS"/. "$dest"/; else echo '{"digest": "sha256:fake"}'; fi
`
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_OC_LOG", logFile)
	t.Setenv("FAKE_OC_CONFIGS", configs)
	t.Setenv("FAKE_OC_FAIL", fail)
	return logFile
}

func TestSync(t *testing.T) {
	calls := installFakeOC(t, "community-operator-index:v4.21")
	dataDir := t.TempDir()
	authFile := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(authFile, []byte("{}"), 0o600)

	var mu sync.Mutex
	var lines []string
	started, done := map[string]bool{}, map[string]bool{}
	result, err := Sync(context.Background(), SyncOptions{
		DataDir:        dataDir,
		RegistryConfig: ResolveRegistryConfig("/does/not/exist", authFile),
		Parallel:       2,
		OCPVersions:    []string{"4.20", "4.21"},
		CatalogTypes:   []string{"redhat-operator-index", "community-operator-index"},
		Log:            func(l string) { mu.Lock(); lines = append(lines, l); mu.Unlock() },
		OnStart:        func(c, v string) { mu.Lock(); started[c+v] = true; mu.Unlock() },
		OnDone:         func(c, v string, ok bool) { mu.Lock(); done[c+v] = ok; mu.Unlock() },
	})

	if err == nil || result != (SyncResult{Total: 4, Successful: 3, Failed: 1}) {
		t.Fatalf("Sync = %+v, %v", result, err)
	}
	if len(started) != 4 || done["community-operator-index4.21"] || !done["redhat-operator-index4.20"] {
		t.Errorf("started=%v done=%v", started, done)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Using registry config: " + authFile,
		"Extracting community-operator-index v4.21 (attempt 3)...",
		"ERROR: Failed to extract registry.redhat.io/redhat/community-operator-index:v4.21 after 3 attempts",
		"error: unauthorized",
		"Completed: 3/4 catalogs successful, 1 failed",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}

	var index catalogIndex
	readJSON(t, filepath.Join(dataDir, "catalog-index.json"), &index)
	if len(index.Catalogs) != 3 || index.Catalogs[0].CatalogType != "redhat-operator-index" || index.Catalogs[0].Digest != "sha256:fake" {
		t.Errorf("index = %+v", index)
	}
	var operators []Operator
	readJSON(t, filepath.Join(SnapshotDir(dataDir, "redhat-operator-index", "4.21"), "operators.json"), &operators)
	if len(operators) != 7 || operators[0].Catalog != "redhat-operator-index" || operators[0].OCPVersion != "v4.21" {
		t.Errorf("operators = %+v", operators)
	}
	if _, err := os.Stat(filepath.Join(SnapshotDir(dataDir, "community-operator-index", "4.21"), "configs")); !os.IsNotExist(err) {
		t.Error("failed extraction left a configs directory behind")
	}

	callLog, _ := os.ReadFile(calls)
	if !strings.Contains(string(callLog), "image extract --registry-config="+authFile+" --path /configs/:") {
		t.Errorf("oc calls:\n%s", callLog)
	}
}

func TestSyncWithoutOC(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := Sync(context.Background(), SyncOptions{DataDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "oc CLI") {
		t.Errorf("err = %v", err)
	}
}
