package server

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Channel is a catalog channel as stored in generated metadata. The catalog
// files contain either bare channel-name strings or {"name": ...} objects.
type Channel struct {
	Name     string
	isString bool
}

func (c *Channel) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		c.Name, c.isString = s, true
		return nil
	}
	var obj struct {
		Name string `json:"name"`
	}
	// Anything other than a string or an object with a name is treated as empty.
	_ = json.Unmarshal(b, &obj)
	c.Name = obj.Name
	return nil
}

func (c Channel) MarshalJSON() ([]byte, error) {
	if c.isString {
		return json.Marshal(c.Name)
	}
	return json.Marshal(struct {
		Name string `json:"name"`
	}{c.Name})
}

// VersionRange is the min/max version of a channel.
type VersionRange struct {
	MinVersion *string `json:"minVersion"`
	MaxVersion *string `json:"maxVersion"`
}

// OperatorEntry is one operator in a generated operators.json file.
type OperatorEntry struct {
	Name                 string                  `json:"name"`
	DefaultChannel       string                  `json:"defaultChannel,omitempty"`
	Channels             []Channel               `json:"channels,omitempty"`
	ChannelVersions      map[string][]string     `json:"channelVersions,omitempty"`
	ChannelVersionRanges map[string]VersionRange `json:"channelVersionRanges,omitempty"`
	AvailableVersions    []string                `json:"availableVersions,omitempty"`
	Catalog              string                  `json:"catalog,omitempty"`
	OCPVersion           string                  `json:"ocpVersion,omitempty"`
	CatalogURL           string                  `json:"catalogUrl,omitempty"`
}

// ChannelObject is a normalized channel returned to the UI.
type ChannelObject struct {
	Name              string
	AvailableVersions []string
	MinVersion        *string
	MaxVersion        *string
	// fromMetadata channels always carry availableVersions and min/max (possibly null).
	fromMetadata bool
}

func (c ChannelObject) MarshalJSON() ([]byte, error) {
	if c.fromMetadata {
		versions := c.AvailableVersions
		if versions == nil {
			versions = []string{}
		}
		return json.Marshal(struct {
			Name              string   `json:"name"`
			AvailableVersions []string `json:"availableVersions"`
			MinVersion        *string  `json:"minVersion"`
			MaxVersion        *string  `json:"maxVersion"`
		}{c.Name, versions, c.MinVersion, c.MaxVersion})
	}
	return json.Marshal(struct {
		Name              string   `json:"name"`
		AvailableVersions []string `json:"availableVersions,omitempty"`
	}{c.Name, c.AvailableVersions})
}

var (
	gitVersionRe     = regexp.MustCompile(`GitVersion:"(\d+\.\d+\.\d+)`)
	anyVersionRe     = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	baseVersionRe    = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)
	genericWithVRe   = regexp.MustCompile(`^[^.]+\.v(.+)`)
	genericNoVRe     = regexp.MustCompile(`^[^.]+\.(\d+\.\d+\.\d+.*)`)
	leadingVersionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)`)
)

func parseOcMirrorVersion(raw string) string {
	if m := gitVersionRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	if m := anyVersionRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return "Not available"
}

func getCatalogNameFromURL(catalogURL string) string {
	switch {
	case strings.Contains(catalogURL, "redhat-operator-index"):
		return "redhat-operator-index"
	case strings.Contains(catalogURL, "certified-operator-index"):
		return "certified-operator-index"
	case strings.Contains(catalogURL, "community-operator-index"):
		return "community-operator-index"
	}
	return "redhat-operator-index"
}

// getCatalogVersionFromURL returns the tag part of a catalog reference, defaulting to v4.21.
func getCatalogVersionFromURL(catalogURL string) string {
	if parts := strings.Split(catalogURL, ":"); len(parts) > 1 {
		return parts[1]
	}
	return "v4.21"
}

func getCatalogDescription(catalogType string) string {
	switch catalogType {
	case "redhat-operator-index":
		return "Red Hat certified operators"
	case "certified-operator-index":
		return "Certified operators from partners"
	case "community-operator-index":
		return "Community operators"
	}
	return "Unknown catalog type"
}

func compareVersionStrings(a, b string) int {
	base := func(v string) string {
		if m := baseVersionRe.FindStringSubmatch(v); m != nil {
			return m[1]
		}
		return v
	}
	partsA := strings.Split(base(a), ".")
	partsB := strings.Split(base(b), ".")
	part := func(parts []string, i int) float64 {
		if i >= len(parts) {
			return 0
		}
		n, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0
		}
		return n
	}
	for i := 0; i < max(len(partsA), len(partsB)); i++ {
		pa, pb := part(partsA, i), part(partsB, i)
		if pa < pb {
			return -1
		}
		if pa > pb {
			return 1
		}
	}
	return strings.Compare(a, b)
}

// sortVersions removes blanks and duplicates and sorts by semantic version. Never returns nil.
func sortVersions(versions []string) []string {
	seen := make(map[string]bool, len(versions))
	out := []string{}
	for _, v := range versions {
		if strings.TrimSpace(v) == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool { return compareVersionStrings(out[i], out[j]) < 0 })
	return out
}

func extractChannelNames(channels []Channel) []string {
	if len(channels) == 0 {
		return []string{}
	}
	if len(channels) == 1 && channels[0].isString {
		only := channels[0].Name
		for _, sep := range []string{"\n", " "} {
			if strings.Contains(only, sep) {
				out := []string{}
				for _, part := range strings.Split(only, sep) {
					if p := strings.TrimSpace(part); p != "" {
						out = append(out, p)
					}
				}
				return out
			}
		}
		return []string{only}
	}
	out := []string{}
	for _, ch := range channels {
		if strings.TrimSpace(ch.Name) != "" {
			out = append(out, ch.Name)
		}
	}
	return out
}

// extractVersionInfo splits channel names into generic channels and versions
// encoded in "<operator>.v<version>" style names.
func extractVersionInfo(channelNames []string, operatorName string) (genericChannels, versions []string) {
	found := []string{}
	genericChannels = []string{}

	matchPrefixed := func(channel, prefix string) (string, bool) {
		if !strings.Contains(channel, prefix+".") {
			return "", false
		}
		quoted := regexp.QuoteMeta(prefix)
		if m := regexp.MustCompile(quoted + `\.v(.+)`).FindStringSubmatch(channel); m != nil {
			return m[1], true
		}
		if m := regexp.MustCompile(quoted + `\.(.+)`).FindStringSubmatch(channel); m != nil {
			return m[1], true
		}
		return "", false
	}

	for _, channel := range channelNames {
		if strings.TrimSpace(channel) == "" {
			continue
		}
		if operatorName != "" {
			if v, ok := matchPrefixed(channel, operatorName); ok {
				found = append(found, v)
				continue
			}
			base := strings.TrimSuffix(strings.TrimSuffix(operatorName, "-certified"), "-community")
			if base != operatorName {
				if v, ok := matchPrefixed(channel, base); ok {
					found = append(found, v)
					continue
				}
			}
		}
		if m := genericWithVRe.FindStringSubmatch(channel); m != nil {
			found = append(found, m[1])
			continue
		}
		if m := genericNoVRe.FindStringSubmatch(channel); m != nil {
			found = append(found, m[1])
			continue
		}
		if m := leadingVersionRe.FindStringSubmatch(channel); m != nil {
			found = append(found, m[1])
			continue
		}
		genericChannels = append(genericChannels, channel)
	}
	return genericChannels, sortVersions(found)
}

func getVersionsFromMetadata(op *OperatorEntry, channelName string) []string {
	if channelName != "" && op.ChannelVersions != nil {
		if versions, ok := op.ChannelVersions[channelName]; ok {
			return sortVersions(versions)
		}
	}
	if channelName == "" {
		if len(op.ChannelVersions) > 0 {
			all := []string{}
			for _, versions := range op.ChannelVersions {
				all = append(all, versions...)
			}
			return sortVersions(all)
		}
		if op.AvailableVersions != nil {
			return sortVersions(op.AvailableVersions)
		}
	}
	_, versions := extractVersionInfo(extractChannelNames(op.Channels), op.Name)
	return versions
}

// normalizeChannels converts raw channels into ChannelObjects. When operator
// metadata with per-channel versions is available it is used directly.
func normalizeChannels(channels []Channel, operatorName string, op *OperatorEntry) []ChannelObject {
	names := extractChannelNames(channels)
	if len(names) == 0 && op != nil && op.ChannelVersions != nil {
		for name := range op.ChannelVersions {
			names = append(names, name)
		}
		sort.Strings(names)
	}
	if len(names) == 0 {
		return []ChannelObject{}
	}

	if op != nil && (op.ChannelVersions != nil || op.ChannelVersionRanges != nil) {
		out := make([]ChannelObject, 0, len(names))
		for _, name := range names {
			co := ChannelObject{Name: name, AvailableVersions: getVersionsFromMetadata(op, name), fromMetadata: true}
			if r, ok := op.ChannelVersionRanges[name]; ok {
				co.MinVersion, co.MaxVersion = r.MinVersion, r.MaxVersion
			}
			out = append(out, co)
		}
		return out
	}

	generic, versions := extractVersionInfo(names, operatorName)
	out := make([]ChannelObject, 0, len(generic))
	for _, name := range generic {
		out = append(out, ChannelObject{Name: name})
	}
	if len(out) > 0 && len(versions) > 0 {
		out[0].AvailableVersions = versions
	}
	return out
}

// channelObjectsFromGeneratedOperator returns {name} channel objects, or nil when op is nil.
func channelObjectsFromGeneratedOperator(op *OperatorEntry) []ChannelObject {
	if op == nil {
		return nil
	}
	out := []ChannelObject{}
	for _, ch := range op.Channels {
		if ch.Name != "" {
			out = append(out, ChannelObject{Name: ch.Name})
		}
	}
	return out
}

// isPathAvailable reports whether targetPath exists and is writable, or could
// be created because its nearest existing ancestor is writable.
func isPathAvailable(targetPath string) bool {
	current, err := filepath.Abs(targetPath)
	if err != nil {
		return false
	}
	for {
		_, err := os.Stat(current)
		if err == nil {
			return syscall.Access(current, 0x2) == nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}
