package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func strs(v ...string) []string { return append([]string{}, v...) }

func channels(names ...string) []Channel {
	out := make([]Channel, len(names))
	for i, n := range names {
		out[i] = Channel{Name: n, isString: true}
	}
	return out
}

func TestParseOcMirrorVersion(t *testing.T) {
	cases := map[string]string{
		`oc-mirror version 1.2.3 GitVersion:"1.2.3"`: "1.2.3",
		"version 2.0.1":   "2.0.1",
		"no version here": "Not available",
	}
	for in, want := range cases {
		if got := parseOcMirrorVersion(in); got != want {
			t.Errorf("parseOcMirrorVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCatalogNameAndDescription(t *testing.T) {
	cases := map[string]string{
		"registry.redhat.io/redhat/redhat-operator-index:v4.21":    "redhat-operator-index",
		"registry.redhat.io/redhat/certified-operator-index:v4.21": "certified-operator-index",
		"registry.redhat.io/redhat/community-operator-index:v4.21": "community-operator-index",
		"unknown/catalog": "redhat-operator-index",
	}
	for in, want := range cases {
		if got := getCatalogNameFromURL(in); got != want {
			t.Errorf("getCatalogNameFromURL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := getCatalogDescription("certified-operator-index"); got != "Certified operators from partners" {
		t.Errorf("unexpected description %q", got)
	}
	if got := getCatalogDescription("unknown"); got != "Unknown catalog type" {
		t.Errorf("unexpected description %q", got)
	}
	if got := getCatalogVersionFromURL("registry.redhat.io/redhat/redhat-operator-index"); got != "v4.21" {
		t.Errorf("default catalog version = %q", got)
	}
}

func TestCompareAndSortVersions(t *testing.T) {
	if compareVersionStrings("1.0.0", "1.0.1") >= 0 || compareVersionStrings("1.1.0", "2.0.0") >= 0 {
		t.Error("expected ascending order")
	}
	if compareVersionStrings("1.0.1", "1.0.0") <= 0 {
		t.Error("expected 1.0.1 > 1.0.0")
	}
	if compareVersionStrings("1.0.0", "1.0.0") != 0 {
		t.Error("expected equal versions")
	}
	if compareVersionStrings("4.21.0-0", "4.21.1") >= 0 {
		t.Error("expected base version comparison")
	}
	if compareVersionStrings("1.10.0", "1.9.0") <= 0 {
		t.Error("expected numeric comparison")
	}
	if got := sortVersions(strs("1.0.1", "1.0.0", "1.0.1", "", "  ")); !reflect.DeepEqual(got, strs("1.0.0", "1.0.1")) {
		t.Errorf("sortVersions = %v", got)
	}
	if got := sortVersions(nil); got == nil || len(got) != 0 {
		t.Errorf("sortVersions(nil) = %#v, want empty non-nil", got)
	}
}

func TestExtractChannelNames(t *testing.T) {
	cases := []struct {
		in   []Channel
		want []string
	}{
		{nil, strs()},
		{channels("stable\nbeta"), strs("stable", "beta")},
		{channels("stable beta"), strs("stable", "beta")},
		{channels("stable"), strs("stable")},
		{[]Channel{{Name: "release-2.16"}, {Name: "stable"}}, strs("release-2.16", "stable")},
		{[]Channel{{Name: "stable", isString: true}, {Name: "beta"}}, strs("stable", "beta")},
	}
	for _, c := range cases {
		if got := extractChannelNames(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("extractChannelNames(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestChannelJSONRoundTrip(t *testing.T) {
	var got []Channel
	if err := json.Unmarshal([]byte(`["stable", {"name": "beta"}, {}, 3]`), &got); err != nil {
		t.Fatal(err)
	}
	if names := extractChannelNames(got); !reflect.DeepEqual(names, strs("stable", "beta")) {
		t.Errorf("names = %v", names)
	}
	out, _ := json.Marshal(got[:2])
	if string(out) != `["stable",{"name":"beta"}]` {
		t.Errorf("marshal = %s", out)
	}
}

func TestExtractVersionInfo(t *testing.T) {
	_, versions := extractVersionInfo(strs("acm.v2.16.0", "acm.v2.15.0"), "acm")
	if !reflect.DeepEqual(versions, strs("2.15.0", "2.16.0")) {
		t.Errorf("versions = %v", versions)
	}
	generic, versions := extractVersionInfo(strs("stable", "beta"), "")
	if !reflect.DeepEqual(generic, strs("stable", "beta")) || len(versions) != 0 {
		t.Errorf("generic = %v, versions = %v", generic, versions)
	}
	_, versions = extractVersionInfo(strs("pkg.v1.0.0"), "")
	if !slices.Contains(versions, "1.0.0") {
		t.Errorf("versions = %v", versions)
	}
	_, versions = extractVersionInfo(strs("foo.v3.1.0"), "foo-certified")
	if !slices.Contains(versions, "3.1.0") {
		t.Errorf("certified suffix versions = %v", versions)
	}
}

func TestNormalizeChannels(t *testing.T) {
	if got := normalizeChannels(nil, "", nil); len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}

	op := &OperatorEntry{Name: "op", ChannelVersions: map[string][]string{"stable": {"1.0.1", "1.0.0"}, "beta": {"1.1.0"}}}
	got := normalizeChannels(nil, "", op)
	if len(got) != 2 || got[1].Name != "stable" || !reflect.DeepEqual(got[1].AvailableVersions, strs("1.0.0", "1.0.1")) {
		t.Errorf("metadata channels = %+v", got)
	}

	minV, maxV := "1.0.0", "1.0.1"
	op = &OperatorEntry{
		Name:                 "op",
		ChannelVersions:      map[string][]string{"stable": {"1.0.0"}},
		ChannelVersionRanges: map[string]VersionRange{"stable": {MinVersion: &minV, MaxVersion: &maxV}},
	}
	b, _ := json.Marshal(normalizeChannels(nil, "", op))
	if string(b) != `[{"name":"stable","availableVersions":["1.0.0"],"minVersion":"1.0.0","maxVersion":"1.0.1"}]` {
		t.Errorf("range channels = %s", b)
	}

	got = normalizeChannels(channels("stable", "pkg.v1.0.0"), "pkg", nil)
	b, _ = json.Marshal(got)
	if string(b) != `[{"name":"stable","availableVersions":["1.0.0"]}]` {
		t.Errorf("generic channels = %s", b)
	}
}

func TestGetVersionsFromMetadata(t *testing.T) {
	op := &OperatorEntry{Name: "op", ChannelVersions: map[string][]string{"stable": {"1.0.0", "1.0.1"}, "beta": {"1.1.0"}}}
	if got := getVersionsFromMetadata(op, "stable"); !reflect.DeepEqual(got, strs("1.0.0", "1.0.1")) {
		t.Errorf("channel versions = %v", got)
	}
	if got := getVersionsFromMetadata(op, ""); !reflect.DeepEqual(got, strs("1.0.0", "1.0.1", "1.1.0")) {
		t.Errorf("all versions = %v", got)
	}
	op = &OperatorEntry{Name: "op", AvailableVersions: strs("2.0.1", "2.0.0")}
	if got := getVersionsFromMetadata(op, ""); !reflect.DeepEqual(got, strs("2.0.0", "2.0.1")) {
		t.Errorf("available versions = %v", got)
	}
	op = &OperatorEntry{Name: "acm", Channels: channels("acm.v2.16.0", "acm.v2.15.0")}
	if got := getVersionsFromMetadata(op, ""); !reflect.DeepEqual(got, strs("2.15.0", "2.16.0")) {
		t.Errorf("channel-derived versions = %v", got)
	}
}

func TestChannelObjectsFromGeneratedOperator(t *testing.T) {
	if channelObjectsFromGeneratedOperator(nil) != nil {
		t.Error("expected nil for nil operator")
	}
	if got := channelObjectsFromGeneratedOperator(&OperatorEntry{}); got == nil || len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
	var op OperatorEntry
	if err := json.Unmarshal([]byte(`{"channels":["valid","",{"name":""},{"name":"also-valid"},{}]}`), &op); err != nil {
		t.Fatal(err)
	}
	got := channelObjectsFromGeneratedOperator(&op)
	if len(got) != 2 || got[0].Name != "valid" || got[1].Name != "also-valid" {
		t.Errorf("got %+v", got)
	}
}

func TestIsPathAvailable(t *testing.T) {
	dir := t.TempDir()
	writable := filepath.Join(dir, "writable")
	if err := os.MkdirAll(writable, 0o755); err != nil {
		t.Fatal(err)
	}
	if !isPathAvailable(writable) {
		t.Error("existing writable dir should be available")
	}
	if !isPathAvailable(filepath.Join(dir, "nested", "does", "not", "exist")) {
		t.Error("missing path under writable parent should be available")
	}
	if os.Geteuid() != 0 && isPathAvailable("/nonexistent-xyz-123-xyz/sub") {
		t.Error("path under unwritable root should not be available")
	}
}

func TestBuildOptionalFlagArgs(t *testing.T) {
	args, err := buildOptionalFlagArgs(map[string]any{
		"removeSignatures": true, "imageTimeout": "10m", "retryDelay": "30s", "retryTimes": float64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strs("--remove-signatures", "--image-timeout", "10m", "--retry-delay", "30s", "--retry-times", "3")
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %v", args)
	}
	for _, bad := range []any{
		"string", []any{}, map[string]any{"unknown": true}, map[string]any{"removeSignatures": "yes"},
		map[string]any{"imageTimeout": "10minutes"}, map[string]any{"imageTimeout": "0s"},
		map[string]any{"retryDelay": 30.0}, map[string]any{"retryTimes": 1.5},
		map[string]any{"retryTimes": -1.0}, map[string]any{"retryTimes": float64(1 << 53)},
	} {
		if _, err := buildOptionalFlagArgs(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
	if seconds, ok := parseDurationSeconds("10m30s"); !ok || seconds != 630 {
		t.Errorf("parseDurationSeconds(10m30s) = %d, %v", seconds, ok)
	}
}

func TestJSONToYAMLPreservesOrderAndTypes(t *testing.T) {
	out, err := jsonToYAML([]byte(`{"kind":"ImageSetConfiguration","apiVersion":"mirror.openshift.io/v2alpha1","mirror":{"platform":{"channels":[{"name":"stable-4.16","minVersion":"4.16","maxVersion":"true"}],"graph":true},"operators":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `kind: ImageSetConfiguration
apiVersion: mirror.openshift.io/v2alpha1
mirror:
  platform:
    channels:
      - name: stable-4.16
        minVersion: "4.16"
        maxVersion: "true"
    graph: true
  operators: []
`
	if out != want {
		t.Errorf("jsonToYAML =\n%s\nwant\n%s", out, want)
	}
}

func TestComputeCatalogDiff(t *testing.T) {
	oldData := &catalogData{keys: strs("a:v1"), operators: map[string][]OperatorEntry{
		"a:v1": {{Name: "kept", AvailableVersions: strs("1.0.0")}, {Name: "gone"}},
	}}
	newData := &catalogData{keys: strs("a:v1"), operators: map[string][]OperatorEntry{
		"a:v1": {{Name: "kept", AvailableVersions: strs("1.0.0", "1.1.0")}, {Name: "fresh"}},
	}}
	diff := computeCatalogDiff(oldData, newData)
	if len(diff) != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	d := diff[0]
	if !reflect.DeepEqual(d.NewOperators, strs("fresh")) || !reflect.DeepEqual(d.RemovedOperators, strs("gone")) ||
		len(d.UpdatedOperators) != 1 || !reflect.DeepEqual(d.UpdatedOperators[0].AddedVersions, strs("1.1.0")) {
		t.Errorf("diff = %+v", d)
	}
}
