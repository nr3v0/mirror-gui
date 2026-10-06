package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type catalogIndexEntry struct {
	CatalogType string `json:"catalog_type"`
	OCPVersion  string `json:"ocp_version"`
	CatalogURL  string `json:"catalog_url"`
	Digest      string `json:"digest,omitempty"`
	SyncedAt    string `json:"synced_at,omitempty"`
}

// catalogData is the pre-fetched operator metadata, keyed by "<catalog_type>:<ocp_version>".
type catalogData struct {
	index     []catalogIndexEntry
	keys      []string // operator keys in index order
	operators map[string][]OperatorEntry
	channels  map[string][]Channel // keyed by "<operator>:<catalog_type>:<ocp_version>"
}

func catalogKey(catalogType, ocpVersion string) string { return catalogType + ":" + ocpVersion }

func (d *catalogData) findOperator(key, name string) *OperatorEntry {
	ops := d.operators[key]
	for i := range ops {
		if ops[i].Name == name {
			return &ops[i]
		}
	}
	return nil
}

// findOperatorAnywhere returns the first operator with this name across all catalogs.
func (d *catalogData) findOperatorAnywhere(name string) *OperatorEntry {
	for _, key := range d.keys {
		if op := d.findOperator(key, name); op != nil {
			return op
		}
	}
	return nil
}

type OperatorDependency struct {
	PackageName         string  `json:"packageName"`
	VersionRange        *string `json:"versionRange"`
	DisplayName         string  `json:"displayName,omitempty"`
	Catalog             string  `json:"catalog,omitempty"`
	CatalogURL          string  `json:"catalogUrl,omitempty"`
	DefaultChannel      string  `json:"defaultChannel,omitempty"`
	IsDependencyPackage bool    `json:"isDependencyPackage,omitempty"`
}

type cachedOperator struct {
	key        string
	name       string
	catalog    string
	ocpVersion string
}

type cachedCatalog struct {
	name, url, description, ocpVersion, digest, syncedAt string
	operatorCount                                        int
}

type operatorCache struct {
	catalogs   []cachedCatalog
	operators  []cachedOperator
	channels   map[string][]Channel
	lastUpdate time.Time
}

const operatorCacheTTL = time.Hour

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func (s *Server) resolveCatalogDataDir() string {
	if f, err := os.Open(filepath.Join(s.cfg.RuntimeCatalogDir, "catalog-index.json")); err == nil {
		f.Close()
		return s.cfg.RuntimeCatalogDir
	}
	return s.cfg.BuiltinCatalogDir
}

func (s *Server) readCatalogIndex() (dir string, index []catalogIndexEntry, err error) {
	dir = s.resolveCatalogDataDir()
	var idx struct {
		Catalogs []catalogIndexEntry `json:"catalogs"`
	}
	if err := readJSONFile(filepath.Join(dir, "catalog-index.json"), &idx); err != nil {
		return dir, nil, err
	}
	return dir, idx.Catalogs, nil
}

// loadCatalogData returns the cached catalog data, loading it on first use.
// Callers must hold catalogMu. Returns nil when no catalog index is available.
func (s *Server) loadCatalogData() *catalogData {
	if s.catalog != nil {
		return s.catalog
	}
	dir, index, err := s.readCatalogIndex()
	if err != nil {
		log.Printf("Error loading pre-fetched catalog data: %v", err)
		return nil
	}
	log.Printf("Using catalog data from %s", dir)

	data := &catalogData{
		index:     index,
		operators: map[string][]OperatorEntry{},
		channels:  map[string][]Channel{},
	}
	total := 0
	for _, c := range index {
		key := catalogKey(c.CatalogType, c.OCPVersion)
		var ops []OperatorEntry
		if err := readJSONFile(filepath.Join(dir, c.CatalogType, c.OCPVersion, "operators.json"), &ops); err != nil {
			log.Printf("Could not load operators for %s: %v", key, err)
			continue
		}
		if _, seen := data.operators[key]; !seen {
			data.keys = append(data.keys, key)
		}
		data.operators[key] = ops
		total += len(ops)
		for _, op := range ops {
			data.channels[op.Name+":"+key] = op.Channels
		}
	}
	log.Printf("Pre-fetched catalog data loaded with %d total operators", total)
	s.catalog = data
	return data
}

// loadDependencies returns per-catalog dependency maps. Callers must hold catalogMu.
func (s *Server) loadDependencies() map[string]map[string][]OperatorDependency {
	if s.deps != nil {
		return s.deps
	}
	dir, index, err := s.readCatalogIndex()
	if err != nil {
		log.Println("Could not load dependencies data, dependency detection may be limited")
		return nil
	}
	merged := map[string]map[string][]OperatorDependency{}
	for _, c := range index {
		var deps map[string][]OperatorDependency
		if readJSONFile(filepath.Join(dir, c.CatalogType, c.OCPVersion, "dependencies.json"), &deps) == nil {
			merged[catalogKey(c.CatalogType, c.OCPVersion)] = deps
		}
	}
	s.deps = merged
	return merged
}

// resetCatalogCaches drops all cached catalog state. Callers must hold catalogMu.
func (s *Server) resetCatalogCaches() {
	s.catalog = nil
	s.deps = nil
	s.opCache = operatorCache{}
}

func (s *Server) operatorCacheValid() bool {
	return !s.opCache.lastUpdate.IsZero() && time.Since(s.opCache.lastUpdate) < operatorCacheTTL
}

var fallbackCatalogs = []cachedCatalog{
	{name: "redhat-operator-index", url: "registry.redhat.io/redhat/redhat-operator-index", description: "Red Hat certified operators"},
	{name: "certified-operator-index", url: "registry.redhat.io/redhat/certified-operator-index", description: "Certified operators from partners"},
	{name: "community-operator-index", url: "registry.redhat.io/redhat/community-operator-index", description: "Community operators"},
}

// updateOperatorCache rebuilds the operator cache when expired. Callers must hold catalogMu.
func (s *Server) updateOperatorCache() *operatorCache {
	if s.operatorCacheValid() {
		return &s.opCache
	}
	cache := operatorCache{channels: map[string][]Channel{}, lastUpdate: time.Now()}
	seen := map[string]bool{}
	add := func(op cachedOperator) {
		if !seen[op.key] {
			seen[op.key] = true
			cache.operators = append(cache.operators, op)
		}
	}

	data := s.loadCatalogData()
	if data != nil && len(data.index) > 0 {
		for _, c := range data.index {
			ops := data.operators[catalogKey(c.CatalogType, c.OCPVersion)]
			cache.catalogs = append(cache.catalogs, cachedCatalog{
				name: c.CatalogType, url: c.CatalogURL, description: getCatalogDescription(c.CatalogType),
				ocpVersion: c.OCPVersion, digest: c.Digest, syncedAt: c.SyncedAt, operatorCount: len(ops),
			})
			for _, op := range ops {
				add(cachedOperator{key: op.Name + ":" + c.CatalogURL, name: op.Name, catalog: c.CatalogURL, ocpVersion: c.OCPVersion})
			}
		}
	} else {
		log.Println("Using fallback static catalogs")
		for _, c := range fallbackCatalogs {
			key := catalogKey(getCatalogNameFromURL(c.url), getCatalogVersionFromURL(c.url))
			var ops []OperatorEntry
			if data != nil {
				ops = data.operators[key]
			}
			c.operatorCount = len(ops)
			cache.catalogs = append(cache.catalogs, c)
			for _, op := range ops {
				add(cachedOperator{key: op.Name, name: op.Name, catalog: c.url})
			}
		}
	}
	log.Printf("Operator cache updated with %d operators", len(cache.operators))
	s.opCache = cache
	return &s.opCache
}

// queryOperatorChannels returns the raw channels of an operator in a catalog reference.
func (s *Server) queryOperatorChannels(catalogURL, operatorName string) []Channel {
	data := s.loadCatalogData()
	if data == nil {
		return nil
	}
	key := catalogKey(getCatalogNameFromURL(catalogURL), getCatalogVersionFromURL(catalogURL))
	if objs := channelObjectsFromGeneratedOperator(data.findOperator(key, operatorName)); len(objs) > 0 {
		out := make([]Channel, len(objs))
		for i, o := range objs {
			out[i] = Channel{Name: o.Name}
		}
		return out
	}
	if ch, ok := data.channels[operatorName+":"+key]; ok {
		return ch
	}
	log.Printf("[ERROR] Channel data not found for %s in %s", operatorName, catalogURL)
	return nil
}

type detailedOperator struct {
	Name           string          `json:"name"`
	DefaultChannel string          `json:"defaultChannel,omitempty"`
	Channels       []ChannelObject `json:"channels"`
	AllChannels    []string        `json:"allChannels"`
	Catalog        string          `json:"catalog,omitempty"`
	OCPVersion     string          `json:"ocpVersion,omitempty"`
	CatalogURL     string          `json:"catalogUrl,omitempty"`
}

func toDetailedOperator(op *OperatorEntry) detailedOperator {
	channels := normalizeChannels(op.Channels, op.Name, op)
	all := make([]string, len(channels))
	for i, ch := range channels {
		all[i] = ch.Name
	}
	return detailedOperator{
		Name: op.Name, DefaultChannel: op.DefaultChannel, Channels: channels, AllChannels: all,
		Catalog: op.Catalog, OCPVersion: op.OCPVersion, CatalogURL: op.CatalogURL,
	}
}

func (s *Server) handleChannels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []string{
		"stable-4.16", "stable-4.17", "stable-4.18", "stable-4.19", "stable-4.20", "stable-4.21", "stable-4.22",
	})
}

func (s *Server) handleCatalogs(w http.ResponseWriter, _ *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.failNextCatalogsGet {
		s.failNextCatalogsGet = false
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to get catalogs"))
		return
	}
	nullable := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	cache := s.updateOperatorCache()
	out := make([]jsonObject, 0, len(cache.catalogs))
	for _, c := range cache.catalogs {
		out = append(out, jsonObject{
			"name": c.name, "url": c.url, "description": c.description,
			"operatorCount": c.operatorCount, "digest": nullable(c.digest), "syncedAt": nullable(c.syncedAt),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleOperators(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if s.failNextOperatorsGet {
		s.failNextOperatorsGet = false
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to get operators"))
		return
	}
	catalog := r.URL.Query().Get("catalog")
	detailed := r.URL.Query().Get("detailed") == "true"

	if catalog == "" {
		seen := map[string]bool{}
		names := []string{}
		for _, op := range s.updateOperatorCache().operators {
			if !seen[op.name] {
				seen[op.name] = true
				names = append(names, op.name)
			}
		}
		writeJSON(w, http.StatusOK, names)
		return
	}

	var ops []OperatorEntry
	found := false
	if data := s.loadCatalogData(); data != nil {
		ops, found = data.operators[catalogKey(getCatalogNameFromURL(catalog), getCatalogVersionFromURL(catalog))]
	}
	if !found {
		for _, op := range s.updateOperatorCache().operators {
			if op.catalog == catalog {
				ops = append(ops, OperatorEntry{Name: op.name, Catalog: op.catalog, OCPVersion: op.ocpVersion})
			}
		}
	}

	if detailed {
		out := make([]detailedOperator, len(ops))
		for i := range ops {
			out[i] = toDetailedOperator(&ops[i])
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	names := make([]string, len(ops))
	for i, op := range ops {
		names[i] = op.Name
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleOperatorsRefresh(w http.ResponseWriter, _ *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	s.opCache.lastUpdate = time.Time{}
	s.updateOperatorCache()
	writeJSON(w, http.StatusOK, jsonObject{"message": "Operator cache refreshed successfully"})
}

func (s *Server) handleOperatorVersions(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	name := r.PathValue("operator")
	catalog := r.URL.Query().Get("catalog")
	channel := r.URL.Query().Get("channel")

	if data := s.loadCatalogData(); data != nil {
		var op *OperatorEntry
		if catalog != "" {
			op = data.findOperator(catalogKey(getCatalogNameFromURL(catalog), getCatalogVersionFromURL(catalog)), name)
		} else {
			op = data.findOperatorAnywhere(name)
		}
		if op != nil {
			writeJSON(w, http.StatusOK, jsonObject{"versions": getVersionsFromMetadata(op, channel)})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, errorBody("Operator not found"))
}

func (s *Server) handleOperatorChannels(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	name := r.PathValue("operator")
	catalogURL := r.URL.Query().Get("catalogUrl")

	if data := s.loadCatalogData(); data != nil {
		var op *OperatorEntry
		if catalogURL != "" {
			op = data.findOperator(catalogKey(getCatalogNameFromURL(catalogURL), getCatalogVersionFromURL(catalogURL)), name)
		} else {
			op = data.findOperatorAnywhere(name)
		}
		if op != nil {
			writeJSON(w, http.StatusOK, toDetailedOperator(op))
			return
		}
	}

	if catalogURL != "" {
		if channels := s.queryOperatorChannels(catalogURL, name); len(channels) > 0 {
			writeJSON(w, http.StatusOK, normalizeChannels(channels, name, nil))
			return
		}
	}

	if channels, ok := s.opCache.channels[name]; ok && s.operatorCacheValid() {
		writeJSON(w, http.StatusOK, normalizeChannels(channels, name, nil))
		return
	}

	cache := s.updateOperatorCache()
	var info *cachedOperator
	for i := range cache.operators {
		if cache.operators[i].name == name {
			info = &cache.operators[i]
			break
		}
	}
	if info == nil {
		writeJSON(w, http.StatusNotFound, errorBody("Operator not found"))
		return
	}
	if channels := s.queryOperatorChannels(info.catalog, name); len(channels) > 0 {
		cache.channels[name] = channels
		writeJSON(w, http.StatusOK, normalizeChannels(channels, name, nil))
		return
	}
	writeJSON(w, http.StatusNotFound, errorBody("No channels found for this operator"))
}

func (s *Server) handleOperatorsChannels(w http.ResponseWriter, r *http.Request) {
	catalogURL := r.URL.Query().Get("catalogUrl")
	name := r.URL.Query().Get("operatorName")
	if catalogURL == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("catalogUrl and operatorName query parameters are required"))
		return
	}
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	if channels := s.queryOperatorChannels(catalogURL, name); len(channels) > 0 {
		writeJSON(w, http.StatusOK, normalizeChannels(channels, name, nil))
		return
	}
	writeJSON(w, http.StatusNotFound, errorBody("No channels found for this operator"))
}

// getOperatorDependencies returns an operator's dependencies, including those
// declared by a companion "<operator>-dependencies" style package. Callers must hold catalogMu.
func (s *Server) getOperatorDependencies(catalogType, catalogVersion, operatorName string) []OperatorDependency {
	key := catalogKey(catalogType, catalogVersion)
	dependencies := []OperatorDependency{}
	depPackage := ""

	if catalogDeps := s.loadDependencies()[key]; catalogDeps != nil {
		dependencies = append(dependencies, catalogDeps[operatorName]...)
		candidates := []string{}
		if base, ok := strings.CutSuffix(operatorName, "-operator"); ok {
			candidates = append(candidates, base+"-dependencies")
		}
		candidates = append(candidates, operatorName+"-dependencies", operatorName+"-dependency", operatorName+"-deps")
		for _, candidate := range candidates {
			if deps, ok := catalogDeps[candidate]; ok {
				dependencies = append(dependencies, deps...)
				depPackage = candidate
				log.Printf("Found %d dependencies in %s for %s", len(deps), candidate, operatorName)
				break
			}
		}
	}

	data := s.loadCatalogData()
	if depPackage != "" && data != nil {
		if info := data.findOperator(key, depPackage); info != nil {
			exists := false
			for _, d := range dependencies {
				if d.PackageName == depPackage {
					exists = true
					break
				}
			}
			if !exists {
				dependencies = append(dependencies, OperatorDependency{
					PackageName: depPackage, DisplayName: info.Name, Catalog: info.Catalog,
					CatalogURL: info.CatalogURL, DefaultChannel: info.DefaultChannel, IsDependencyPackage: true,
				})
			}
		}
	}

	seen := map[string]bool{}
	unique := []OperatorDependency{}
	for _, d := range dependencies {
		if seen[d.PackageName] {
			continue
		}
		seen[d.PackageName] = true
		if data != nil {
			if info := data.findOperator(key, d.PackageName); info != nil {
				d.DisplayName = firstNonEmpty(d.DisplayName, info.Name)
				d.Catalog = firstNonEmpty(d.Catalog, info.Catalog)
				d.CatalogURL = firstNonEmpty(d.CatalogURL, info.CatalogURL)
				d.DefaultChannel = firstNonEmpty(d.DefaultChannel, info.DefaultChannel)
			}
		}
		unique = append(unique, d)
	}
	return unique
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) handleOperatorDependencies(w http.ResponseWriter, r *http.Request) {
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	name := r.PathValue("operator")
	catalogURL := r.URL.Query().Get("catalogUrl")

	var deps []OperatorDependency
	var catalogType, catalogVersion string
	if catalogURL != "" {
		catalogType, catalogVersion = getCatalogNameFromURL(catalogURL), getCatalogVersionFromURL(catalogURL)
		deps = s.getOperatorDependencies(catalogType, catalogVersion, name)
	} else if data := s.loadCatalogData(); data != nil {
		for _, c := range data.index {
			if found := s.getOperatorDependencies(c.CatalogType, c.OCPVersion, name); len(found) > 0 {
				deps, catalogType, catalogVersion = found, c.CatalogType, c.OCPVersion
				break
			}
		}
	}

	if len(deps) == 0 {
		writeJSON(w, http.StatusOK, jsonObject{
			"operator": name, "dependencies": []OperatorDependency{}, "message": "No dependencies found for this operator",
		})
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{
		"operator": name, "catalogType": catalogType, "catalogVersion": catalogVersion,
		"dependencies": deps, "count": len(deps),
	})
}
