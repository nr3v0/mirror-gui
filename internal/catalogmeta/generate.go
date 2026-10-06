// Package catalogmeta turns extracted operator File-Based Catalog (FBC) configs
// into the operators.json / dependencies.json / catalog-index.json metadata the
// server serves, and syncs that metadata from registry.redhat.io with `oc`.
package catalogmeta

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	supportedExtensions = map[string]bool{".json": true, ".yaml": true, ".yml": true}
	packageFiles        = map[string]bool{"package.json": true, "package.yaml": true, "package.yml": true}
	catalogInlineFiles  = map[string]bool{
		"catalog.json": true, "index.json": true, "catalog.yaml": true,
		"catalog.yml": true, "index.yaml": true, "index.yml": true,
	}
	ignoredFiles = map[string]bool{"released-bundles.json": true}
)

// VersionRange is the lowest and highest version of a channel (both null when empty).
type VersionRange struct {
	MinVersion *string `json:"minVersion"`
	MaxVersion *string `json:"maxVersion"`
}

// Operator is one entry of operators.json.
type Operator struct {
	Name                 string                  `json:"name"`
	DefaultChannel       *string                 `json:"defaultChannel"`
	Channels             []string                `json:"channels"`
	ChannelVersions      map[string][]string     `json:"channelVersions"`
	ChannelVersionRanges map[string]VersionRange `json:"channelVersionRanges"`
	AvailableVersions    []string                `json:"availableVersions"`
	MinVersion           *string                 `json:"minVersion"`
	MaxVersion           *string                 `json:"maxVersion"`
	Catalog              string                  `json:"catalog"`
	OCPVersion           string                  `json:"ocpVersion"`
	CatalogURL           string                  `json:"catalogUrl"`
}

// Dependency is a package an operator's bundle requires (olm.package.required).
type Dependency struct {
	PackageName  string  `json:"packageName"`
	VersionRange *string `json:"versionRange"`
}

// CatalogURL returns the registry.redhat.io reference for a catalog type and "v"-prefixed OCP version.
func CatalogURL(catalogType, ocpVersion string) string {
	return fmt.Sprintf("registry.redhat.io/redhat/%s:%s", catalogType, ocpVersion)
}

func normalizeString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// uniqueStrings returns the sorted, de-duplicated non-empty values.
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

var (
	baseVersionRe   = regexp.MustCompile(`^(\d+\.\d+\.\d+)`)
	numericDottedRe = regexp.MustCompile(`^\d+(?:\.\d+)*$`)
	leadingNumberRe = regexp.MustCompile(`^(\d+)(.*)`)
)

// compareDigits compares two non-negative decimal integers of any length.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// CompareVersions orders "X.Y.Z[suffix]" versions: numerically by X.Y.Z, then
// a bare version before one with a suffix, then by the suffix's leading number
// and remaining text.
func CompareVersions(left, right string) int {
	base := func(v string) string {
		if m := baseVersionRe.FindStringSubmatch(v); m != nil {
			return m[1]
		}
		return v
	}
	parts := func(b string) []string {
		if numericDottedRe.MatchString(b) {
			return strings.Split(b, ".")
		}
		return nil
	}
	leftBase, rightBase := base(left), base(right)
	leftParts, rightParts := parts(leftBase), parts(rightBase)
	for i := 0; i < max(len(leftParts), len(rightParts)); i++ {
		l, r := "0", "0"
		if i < len(leftParts) {
			l = leftParts[i]
		}
		if i < len(rightParts) {
			r = rightParts[i]
		}
		if c := compareDigits(l, r); c != 0 {
			return c
		}
	}

	leftSuffix, rightSuffix := left[len(leftBase):], right[len(rightBase):]
	if leftSuffix == rightSuffix {
		return 0
	}
	if leftSuffix == "" {
		return -1
	}
	if rightSuffix == "" {
		return 1
	}
	splitSuffix := func(s string) (string, string) {
		s = strings.TrimLeft(s, "-")
		if m := leadingNumberRe.FindStringSubmatch(s); m != nil {
			return m[1], m[2]
		}
		return "0", s
	}
	leftNum, leftRest := splitSuffix(leftSuffix)
	rightNum, rightRest := splitSuffix(rightSuffix)
	if c := compareDigits(leftNum, rightNum); c != 0 {
		return c
	}
	return strings.Compare(leftRest, rightRest)
}

// SortVersions returns the de-duplicated non-empty versions in CompareVersions
// order. Versions that compare equal keep lexical order.
func SortVersions(values []string) []string {
	out := uniqueStrings(values)
	sort.SliceStable(out, func(i, j int) bool { return CompareVersions(out[i], out[j]) < 0 })
	return out
}

func versionRange(versions []string) VersionRange {
	if len(versions) == 0 {
		return VersionRange{}
	}
	first, last := versions[0], versions[len(versions)-1]
	return VersionRange{MinVersion: &first, MaxVersion: &last}
}

var versionFromNamePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\.v(\d+\.\d+\.\d+(?:[-+._a-zA-Z0-9]*)?)$`),
	regexp.MustCompile(`\.(\d+\.\d+\.\d+(?:[-+._a-zA-Z0-9]*)?)$`),
	regexp.MustCompile(`^v?(\d+\.\d+\.\d+(?:[-+._a-zA-Z0-9]*)?)$`),
}

// extractVersionFromName finds the version in a bundle name like "foo.v1.2.3".
func extractVersionFromName(name string) string {
	for _, re := range versionFromNamePatterns {
		if m := re.FindStringSubmatch(name); m != nil {
			return m[1]
		}
	}
	return ""
}

type document = map[string]any

// normalizeYAML converts map[any]any (YAML maps with non-string keys) into
// map[string]any so YAML and JSON documents can be handled the same way.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeYAML(val)
		}
		return t
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = normalizeYAML(val)
		}
		return out
	case []any:
		for i, val := range t {
			t[i] = normalizeYAML(val)
		}
		return t
	}
	return v
}

// flatten returns the objects in a list of documents, expanding top-level arrays.
func flatten(docs []any) []document {
	out := []document{}
	for _, d := range docs {
		switch t := d.(type) {
		case []any:
			for _, item := range t {
				if m, ok := item.(map[string]any); ok {
					out = append(out, m)
				}
			}
		case map[string]any:
			out = append(out, t)
		}
	}
	return out
}

// loadDocuments parses a JSON file (possibly several concatenated documents)
// or a multi-document YAML file.
func loadDocuments(path string) ([]document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var docs []any
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		dec := json.NewDecoder(bytes.NewReader(data))
		for {
			var doc any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			docs = append(docs, doc)
		}
	case ".yaml", ".yml":
		dec := yaml.NewDecoder(bytes.NewReader(data))
		for {
			var doc any
			if err := dec.Decode(&doc); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return nil, err
			}
			if doc != nil {
				docs = append(docs, normalizeYAML(doc))
			}
		}
	}
	return flatten(docs), nil
}

type category string

const (
	packageExplicit category = "package_explicit"
	channelExplicit category = "channel_explicit"
	bundleExplicit  category = "bundle_explicit"
	catalogInline   category = "catalog_inline"
	otherCategory   category = "other"
)

// sourceCategory classifies a file by its name and directory, relative to the operator directory.
func sourceCategory(relPath string) category {
	name := strings.ToLower(filepath.Base(relPath))
	parts := strings.Split(strings.ToLower(filepath.ToSlash(relPath)), "/")
	hasPart := func(p string) bool {
		for _, part := range parts {
			if part == p {
				return true
			}
		}
		return false
	}
	switch {
	case packageFiles[name]:
		return packageExplicit
	case hasPart("channels") || strings.HasPrefix(name, "channel"):
		return channelExplicit
	case hasPart("bundles") || strings.HasPrefix(name, "bundle"):
		return bundleExplicit
	case catalogInlineFiles[name]:
		return catalogInline
	}
	return otherCategory
}

func isPackageDoc(doc document, relPath string) bool {
	if doc["schema"] == "olm.package" {
		return true
	}
	return packageFiles[strings.ToLower(filepath.Base(relPath))] && normalizeString(doc["name"]) != ""
}

func isChannelDoc(doc document, relPath string) bool {
	if doc["schema"] == "olm.channel" {
		return true
	}
	_, isList := doc["entries"].([]any)
	return sourceCategory(relPath) == channelExplicit && normalizeString(doc["name"]) != "" && isList
}

func isBundleDoc(doc document, relPath string) bool {
	if doc["schema"] == "olm.bundle" {
		return true
	}
	_, isList := doc["properties"].([]any)
	return sourceCategory(relPath) == bundleExplicit && normalizeString(doc["name"]) != "" && isList
}

// collectStructuredFiles lists JSON/YAML files under dir in sorted path order.
func collectStructuredFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return nil
		}
		if !supportedExtensions[strings.ToLower(filepath.Ext(path))] || ignoredFiles[d.Name()] {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// normalizeDependencies de-duplicates dependencies by (package, range) and sorts them.
func normalizeDependencies(deps []map[string]any) []Dependency {
	type key struct {
		pkg   string
		rng   string
		isNil bool
	}
	byKey := map[key]Dependency{}
	var order []key
	for _, dep := range deps {
		pkg := normalizeString(dep["packageName"])
		if pkg == "" {
			continue
		}
		k := key{pkg: pkg, isNil: true}
		d := Dependency{PackageName: pkg}
		if raw, ok := dep["versionRange"]; ok && raw != nil {
			rng, isString := raw.(string)
			if !isString {
				rng = fmt.Sprint(raw)
			}
			k = key{pkg: pkg, rng: rng}
			d.VersionRange = &rng
		}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = d
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].pkg != order[j].pkg {
			return order[i].pkg < order[j].pkg
		}
		return order[i].rng < order[j].rng
	})
	out := make([]Dependency, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out
}

func listOf(v any) []any {
	l, _ := v.([]any)
	return l
}

func extractBundleVersion(bundle document) string {
	for _, p := range listOf(bundle["properties"]) {
		prop, ok := p.(map[string]any)
		if !ok || prop["type"] != "olm.package" {
			continue
		}
		if value, ok := prop["value"].(map[string]any); ok {
			if version := normalizeString(value["version"]); version != "" {
				return version
			}
		}
	}
	return extractVersionFromName(normalizeString(bundle["name"]))
}

type record struct {
	kind     string
	category category
	doc      document
}

// chooseDocs prefers documents of a kind from their dedicated files, falling back to all of that kind.
func chooseDocs(records []record, preferred category, kind string) []record {
	var preferredDocs, all []record
	for _, r := range records {
		if r.kind != kind {
			continue
		}
		all = append(all, r)
		if r.category == preferred {
			preferredDocs = append(preferredDocs, r)
		}
	}
	if len(preferredDocs) > 0 {
		return preferredDocs
	}
	return all
}

type bundleInfo struct {
	doc     document
	version string
}

type versionedBundle struct {
	version string
	doc     document
}

// latestBundle returns the bundle with the highest version; later entries win ties.
func latestBundle(candidates []versionedBundle) document {
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return CompareVersions(candidates[i].version, candidates[j].version) < 0
	})
	return candidates[len(candidates)-1].doc
}

func strPtr(s string) *string { return &s }

// BuildOperator builds the metadata for one operator directory. It returns a
// nil operator when the directory contains no package, channel or bundle documents.
func BuildOperator(operatorDir, catalogType, ocpVersion string) (*Operator, []Dependency, []string) {
	var records []record
	var warnings []string

	files, err := collectStructuredFiles(operatorDir)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(operatorDir), err))
	}
	for _, path := range files {
		rel, _ := filepath.Rel(operatorDir, path)
		cat := sourceCategory(rel)
		docs, err := loadDocuments(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		for _, doc := range docs {
			if isPackageDoc(doc, rel) {
				records = append(records, record{"package", cat, doc})
			}
			if isChannelDoc(doc, rel) {
				records = append(records, record{"channel", cat, doc})
			}
			if isBundleDoc(doc, rel) {
				records = append(records, record{"bundle", cat, doc})
			}
		}
	}

	packageRecords := chooseDocs(records, packageExplicit, "package")
	channelRecords := chooseDocs(records, channelExplicit, "channel")
	bundleRecords := chooseDocs(records, bundleExplicit, "bundle")
	if len(packageRecords) == 0 && len(channelRecords) == 0 && len(bundleRecords) == 0 {
		return nil, nil, warnings
	}

	packageDoc := document{}
	if len(packageRecords) > 0 {
		packageDoc = packageRecords[0].doc
	}
	name := normalizeString(packageDoc["name"])
	for _, recs := range [][]record{channelRecords, bundleRecords} {
		for _, r := range recs {
			if name != "" {
				break
			}
			name = normalizeString(r.doc["package"])
		}
	}
	if name == "" {
		name = filepath.Base(operatorDir)
	}
	defaultChannel := normalizeString(packageDoc["defaultChannel"])

	bundleByName := map[string]bundleInfo{}
	var bundleOrder []string
	var allBundleVersions []string
	for _, r := range bundleRecords {
		bundleName := normalizeString(r.doc["name"])
		if bundleName == "" {
			continue
		}
		version := extractBundleVersion(r.doc)
		if version != "" {
			allBundleVersions = append(allBundleVersions, version)
		}
		if _, seen := bundleByName[bundleName]; !seen {
			bundleOrder = append(bundleOrder, bundleName)
		}
		bundleByName[bundleName] = bundleInfo{doc: r.doc, version: version}
	}

	channelDocs := map[string][]document{}
	var channelNames []string
	for _, r := range channelRecords {
		channelName := normalizeString(r.doc["name"])
		if channelName == "" {
			continue
		}
		if _, seen := channelDocs[channelName]; !seen {
			channelNames = append(channelNames, channelName)
		}
		channelDocs[channelName] = append(channelDocs[channelName], r.doc)
	}

	channels := uniqueStrings(channelNames)
	if len(channels) == 0 && defaultChannel != "" {
		channels = []string{defaultChannel}
	}

	// forEachEntry calls fn with the name of every channel entry.
	forEachEntry := func(docs []document, fn func(entryName string)) {
		for _, doc := range docs {
			for _, e := range listOf(doc["entries"]) {
				entry, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if entryName := normalizeString(entry["name"]); entryName != "" {
					fn(entryName)
				}
			}
		}
	}

	channelVersions := map[string][]string{}
	var flattened []string
	for _, channelName := range channelNames {
		var versions []string
		forEachEntry(channelDocs[channelName], func(entryName string) {
			version := bundleByName[entryName].version
			if version == "" {
				version = extractVersionFromName(entryName)
			}
			if version != "" {
				versions = append(versions, version)
			}
		})
		channelVersions[channelName] = SortVersions(versions)
		flattened = append(flattened, channelVersions[channelName]...)
	}
	if len(flattened) == 0 {
		flattened = allBundleVersions
	}
	availableVersions := SortVersions(flattened)

	if len(channelVersions) == 0 && defaultChannel != "" && len(availableVersions) > 0 {
		channelVersions[defaultChannel] = availableVersions
	}
	for _, ch := range channels {
		if _, ok := channelVersions[ch]; !ok {
			channelVersions[ch] = []string{}
		}
	}
	ranges := make(map[string]VersionRange, len(channelVersions))
	for ch, versions := range channelVersions {
		ranges[ch] = versionRange(versions)
	}

	var selected document
	if docs, ok := channelDocs[defaultChannel]; ok && defaultChannel != "" {
		var candidates []versionedBundle
		forEachEntry(docs, func(entryName string) {
			if info, ok := bundleByName[entryName]; ok && info.version != "" {
				candidates = append(candidates, versionedBundle{info.version, info.doc})
			}
		})
		selected = latestBundle(candidates)
	}
	if selected == nil {
		var candidates []versionedBundle
		for _, bundleName := range bundleOrder {
			if info := bundleByName[bundleName]; info.version != "" {
				candidates = append(candidates, versionedBundle{info.version, info.doc})
			}
		}
		selected = latestBundle(candidates)
	}

	var dependencies []Dependency
	if selected != nil {
		var required []map[string]any
		for _, p := range listOf(selected["properties"]) {
			prop, ok := p.(map[string]any)
			if !ok || prop["type"] != "olm.package.required" {
				continue
			}
			if value, ok := prop["value"].(map[string]any); ok {
				required = append(required, map[string]any{
					"packageName": value["packageName"], "versionRange": value["versionRange"],
				})
			}
		}
		dependencies = normalizeDependencies(required)
	}

	op := &Operator{
		Name:                 name,
		Channels:             channels,
		ChannelVersions:      channelVersions,
		ChannelVersionRanges: ranges,
		AvailableVersions:    availableVersions,
		Catalog:              catalogType,
		OCPVersion:           ocpVersion,
		CatalogURL:           CatalogURL(catalogType, ocpVersion),
	}
	if defaultChannel != "" {
		op.DefaultChannel = strPtr(defaultChannel)
	}
	if r := versionRange(availableVersions); r.MinVersion != nil {
		op.MinVersion, op.MaxVersion = r.MinVersion, r.MaxVersion
	}
	return op, dependencies, warnings
}

// GenerateSnapshot builds metadata for every operator directory under <catalogDir>/configs.
func GenerateSnapshot(catalogDir, catalogType, ocpVersion string) ([]Operator, map[string][]Dependency, []string) {
	operators := []Operator{}
	dependencies := map[string][]Dependency{}
	configsDir := filepath.Join(catalogDir, "configs")

	entries, err := os.ReadDir(configsDir)
	if err != nil {
		return operators, dependencies, []string{"Missing configs directory: " + configsDir}
	}
	var warnings []string
	for _, e := range entries {
		dir := filepath.Join(configsDir, e.Name())
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		op, deps, opWarnings := BuildOperator(dir, catalogType, ocpVersion)
		warnings = append(warnings, opWarnings...)
		if op == nil {
			continue
		}
		operators = append(operators, *op)
		if len(deps) > 0 {
			dependencies[op.Name] = deps
		}
	}
	sort.SliceStable(operators, func(i, j int) bool { return operators[i].Name < operators[j].Name })
	return operators, dependencies, warnings
}

// WriteJSON writes v as indented JSON with a trailing newline, creating parent directories.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
