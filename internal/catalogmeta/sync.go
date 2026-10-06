package catalogmeta

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Catalogs synced by default: every catalog type for every supported OCP version.
var (
	DefaultOCPVersions  = []string{"4.16", "4.17", "4.18", "4.19", "4.20", "4.21", "4.22"}
	DefaultCatalogTypes = []string{"redhat-operator-index", "certified-operator-index", "community-operator-index"}
)

// CatalogInfo is the per-snapshot catalog-info.json, also listed in catalog-index.json.
type CatalogInfo struct {
	CatalogType   string `json:"catalog_type"`
	OCPVersion    string `json:"ocp_version"`
	CatalogURL    string `json:"catalog_url"`
	OperatorCount int    `json:"operator_count"`
	Digest        string `json:"digest"`
	SyncedAt      string `json:"synced_at"`
}

type catalogIndex struct {
	OCPVersions  []string      `json:"ocp_versions"`
	CatalogTypes []string      `json:"catalog_types"`
	Catalogs     []CatalogInfo `json:"catalogs"`
}

// SnapshotDir is <dataDir>/<catalogType>/v<ocpVersion>.
func SnapshotDir(dataDir, catalogType, ocpVersion string) string {
	return filepath.Join(dataDir, catalogType, "v"+ocpVersion)
}

// FinalizeSnapshot generates operators.json, dependencies.json and
// catalog-info.json from <snapshot>/configs and then removes the configs.
// Warnings found while parsing are passed to logf.
func FinalizeSnapshot(dataDir, catalogType, ocpVersion, digest string, logf func(string)) (int, error) {
	dir := SnapshotDir(dataDir, catalogType, ocpVersion)
	configs := filepath.Join(dir, "configs")
	defer os.RemoveAll(configs)

	if info, err := os.Stat(configs); err != nil || !info.IsDir() {
		return 0, fmt.Errorf("no configs directory for %s v%s", catalogType, ocpVersion)
	}
	version := "v" + ocpVersion
	operators, dependencies, warnings := GenerateSnapshot(dir, catalogType, version)
	for _, w := range warnings {
		logf("[WARNING] " + w)
	}
	if err := WriteJSON(filepath.Join(dir, "operators.json"), operators); err != nil {
		return 0, err
	}
	if err := WriteJSON(filepath.Join(dir, "dependencies.json"), dependencies); err != nil {
		return 0, err
	}
	logf(fmt.Sprintf("Generated metadata for %d operators in %s %s", len(operators), catalogType, version))
	info := CatalogInfo{
		CatalogType:   catalogType,
		OCPVersion:    version,
		CatalogURL:    CatalogURL(catalogType, version),
		OperatorCount: len(operators),
		Digest:        digest,
		SyncedAt:      time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	return len(operators), WriteJSON(filepath.Join(dir, "catalog-info.json"), info)
}

// WriteIndex writes catalog-index.json listing every snapshot that has a catalog-info.json.
func WriteIndex(dataDir string, ocpVersions, catalogTypes []string) error {
	index := catalogIndex{OCPVersions: ocpVersions, CatalogTypes: catalogTypes, Catalogs: []CatalogInfo{}}
	for _, version := range ocpVersions {
		for _, catalogType := range catalogTypes {
			data, err := os.ReadFile(filepath.Join(SnapshotDir(dataDir, catalogType, version), "catalog-info.json"))
			if err != nil {
				continue
			}
			var info CatalogInfo
			if err := json.Unmarshal(data, &info); err != nil {
				return fmt.Errorf("%s v%s catalog-info.json: %w", catalogType, version, err)
			}
			index.Catalogs = append(index.Catalogs, info)
		}
	}
	return WriteJSON(filepath.Join(dataDir, "catalog-index.json"), index)
}

// SyncOptions configures Sync.
type SyncOptions struct {
	DataDir        string
	RegistryConfig string // docker/podman auth file; empty uses the default credential locations
	Registry       string // registry to pull catalogs from; default registry.redhat.io
	Parallel       int
	OCPVersions    []string
	CatalogTypes   []string
	Attempts       int
	RetryDelay     time.Duration

	// Log receives progress lines. OnStart and OnDone, when set, are called as
	// each catalog starts extracting and when it finishes.
	Log     func(line string)
	OnStart func(catalogType, ocpVersion string)
	OnDone  func(catalogType, ocpVersion string, ok bool)
}

// SyncResult counts synced catalogs.
type SyncResult struct {
	Total, Successful, Failed int
}

func (o *SyncOptions) defaults() {
	if o.Registry == "" {
		o.Registry = "registry.redhat.io"
	}
	if o.Parallel < 1 {
		o.Parallel = 3
	}
	if o.OCPVersions == nil {
		o.OCPVersions = DefaultOCPVersions
	}
	if o.CatalogTypes == nil {
		o.CatalogTypes = DefaultCatalogTypes
	}
	if o.Attempts < 1 {
		o.Attempts = 3
	}
	if o.Log == nil {
		o.Log = func(string) {}
	}
}

// ResolveRegistryConfig returns the first of the candidate auth files that exists.
func ResolveRegistryConfig(candidates ...string) string {
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	return ""
}

// extractOnce pulls a catalog image and writes its /configs into dest. It
// returns the image's manifest digest, or "unknown" if it cannot be computed.
func (o *SyncOptions) extractOnce(ctx context.Context, ref, dest string) (string, error) {
	img, err := o.pullImage(ctx, ref)
	if err != nil {
		return "", err
	}
	if err := extractConfigs(img, dest); err != nil {
		return "", err
	}
	digest, err := img.Digest()
	if err != nil {
		return "unknown", nil
	}
	return digest.String(), nil
}

// extract copies /configs from a catalog image into the snapshot directory,
// retrying on failure, and returns the image digest.
func (o *SyncOptions) extract(ctx context.Context, catalogType, ocpVersion string) (string, error) {
	ref := fmt.Sprintf("%s/redhat/%s:v%s", o.Registry, catalogType, ocpVersion)
	configs := filepath.Join(SnapshotDir(o.DataDir, catalogType, ocpVersion), "configs")
	var lastErr error
	for attempt := 1; attempt <= o.Attempts; attempt++ {
		o.Log(fmt.Sprintf("Extracting %s v%s (attempt %d)...", catalogType, ocpVersion, attempt))
		os.RemoveAll(configs)
		if err := os.MkdirAll(configs, 0o755); err != nil {
			return "", err
		}
		digest, err := o.extractOnce(ctx, ref, configs)
		if err == nil {
			return digest, nil
		}
		lastErr = err
		os.RemoveAll(configs)
		if attempt < o.Attempts {
			o.Log(fmt.Sprintf("Extracting %s v%s attempt %d failed: %v", catalogType, ocpVersion, attempt, err))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(o.RetryDelay):
			}
		}
	}
	return "", fmt.Errorf("after %d attempts: %w", o.Attempts, lastErr)
}

func (o *SyncOptions) syncOne(ctx context.Context, catalogType, ocpVersion string) bool {
	digest, err := o.extract(ctx, catalogType, ocpVersion)
	if err != nil {
		o.Log(fmt.Sprintf("ERROR: Failed to extract %s %v", CatalogURL(catalogType, "v"+ocpVersion), err))
		return false
	}
	if _, err := FinalizeSnapshot(o.DataDir, catalogType, ocpVersion, digest, o.Log); err != nil {
		o.Log(fmt.Sprintf("ERROR: Failed to generate metadata for %s v%s: %v", catalogType, ocpVersion, err))
		return false
	}
	return true
}

// Sync pulls every catalog image, extracts its /configs, generates its metadata
// and rewrites catalog-index.json. It returns an error when any catalog failed;
// catalogs that succeeded are still written.
func Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	opts.defaults()
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return SyncResult{}, err
	}
	if opts.RegistryConfig != "" {
		if _, err := loadAuthFile(opts.RegistryConfig); err != nil {
			return SyncResult{}, fmt.Errorf("reading registry config: %w", err)
		}
		opts.Log("Using registry config: " + opts.RegistryConfig)
	} else {
		opts.Log("No explicit registry config; using default container registry credentials")
	}
	opts.Log("Output directory: " + opts.DataDir)

	result := SyncResult{Total: len(opts.OCPVersions) * len(opts.CatalogTypes)}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, opts.Parallel)
	for _, version := range opts.OCPVersions {
		for _, catalogType := range opts.CatalogTypes {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				if opts.OnStart != nil {
					opts.OnStart(catalogType, version)
				}
				ok := opts.syncOne(ctx, catalogType, version)
				mu.Lock()
				if ok {
					result.Successful++
				} else {
					result.Failed++
				}
				mu.Unlock()
				if opts.OnDone != nil {
					opts.OnDone(catalogType, version, ok)
				}
			}()
		}
	}
	wg.Wait()

	if err := WriteIndex(opts.DataDir, opts.OCPVersions, opts.CatalogTypes); err != nil {
		return result, err
	}
	opts.Log(fmt.Sprintf("Completed: %d/%d catalogs successful, %d failed", result.Successful, result.Total, result.Failed))
	if result.Failed > 0 {
		return result, fmt.Errorf("%d of %d catalogs failed to sync", result.Failed, result.Total)
	}
	return result, nil
}
