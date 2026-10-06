package server

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nr3v0/mirror-gui/internal/catalogmeta"
)

type updatedOperator struct {
	Name          string   `json:"name"`
	AddedVersions []string `json:"addedVersions"`
}

type catalogSyncDiffEntry struct {
	Catalog          string            `json:"catalog"`
	NewOperators     []string          `json:"newOperators"`
	RemovedOperators []string          `json:"removedOperators"`
	UpdatedOperators []updatedOperator `json:"updatedOperators"`
}

type catalogSyncState struct {
	Status             string                 `json:"status"`
	LastSyncTime       *string                `json:"lastSyncTime"`
	SyncStartTime      *string                `json:"syncStartTime"`
	SuccessCount       int                    `json:"successCount"`
	FailedCount        int                    `json:"failedCount"`
	TotalCount         int                    `json:"totalCount"`
	CompletedCatalogs  int                    `json:"completedCatalogs"`
	CurrentCatalog     *string                `json:"currentCatalog"`
	Error              *string                `json:"error"`
	Logs               []string               `json:"logs"`
	Diff               []catalogSyncDiffEntry `json:"diff"`
	HasRuntimeSyncData bool                   `json:"hasRuntimeSyncData"`
}

func (s *Server) newCatalogSyncState() catalogSyncState {
	return catalogSyncState{
		Status:     "idle",
		TotalCount: len(s.syncVersions) * len(s.syncCatalogTypes),
		Logs:       []string{},
		Diff:       []catalogSyncDiffEntry{},
	}
}

func strPtr(s string) *string { return &s }

func computeCatalogDiff(oldData, newData *catalogData) []catalogSyncDiffEntry {
	diff := []catalogSyncDiffEntry{}
	keys := []string{}
	seenKey := map[string]bool{}
	for _, k := range append(append([]string{}, oldData.keys...), newData.keys...) {
		if !seenKey[k] {
			seenKey[k] = true
			keys = append(keys, k)
		}
	}
	for _, key := range keys {
		oldOps, newOps := oldData.operators[key], newData.operators[key]
		oldByName := make(map[string]*OperatorEntry, len(oldOps))
		for i := range oldOps {
			oldByName[oldOps[i].Name] = &oldOps[i]
		}
		newNames := make(map[string]bool, len(newOps))
		entry := catalogSyncDiffEntry{Catalog: key, NewOperators: []string{}, RemovedOperators: []string{}, UpdatedOperators: []updatedOperator{}}
		for _, op := range newOps {
			newNames[op.Name] = true
			old, existed := oldByName[op.Name]
			if !existed {
				entry.NewOperators = append(entry.NewOperators, op.Name)
				continue
			}
			oldVersions := map[string]bool{}
			for _, v := range old.AvailableVersions {
				oldVersions[v] = true
			}
			added := []string{}
			for _, v := range op.AvailableVersions {
				if !oldVersions[v] {
					added = append(added, v)
				}
			}
			if len(added) > 0 {
				entry.UpdatedOperators = append(entry.UpdatedOperators, updatedOperator{op.Name, added})
			}
		}
		for _, op := range oldOps {
			if !newNames[op.Name] {
				entry.RemovedOperators = append(entry.RemovedOperators, op.Name)
			}
		}
		if len(entry.NewOperators) > 0 || len(entry.RemovedOperators) > 0 || len(entry.UpdatedOperators) > 0 {
			diff = append(diff, entry)
		}
	}
	return diff
}

func (s *Server) appendSyncLog(line string) {
	log.Printf("[catalog-sync] %s", line)
	s.syncMu.Lock()
	s.syncState.Logs = append(s.syncState.Logs, line)
	s.syncMu.Unlock()
}

func (s *Server) handleCatalogSync(w http.ResponseWriter, _ *http.Request) {
	// Hold syncMu from the "already running" check until the state is marked
	// running so concurrent requests cannot start two syncs.
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncState.Status == "running" {
		writeJSON(w, http.StatusConflict, errorBody("Catalog sync is already running"))
		return
	}
	if s.currentPullSecretPath() == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("Pull secret not configured. Please add a pull secret in the Pull Secret tab first."))
		return
	}
	if _, err := exec.LookPath("oc"); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("Catalog sync is not available. The oc CLI is missing from this installation."))
		return
	}

	s.syncState = s.newCatalogSyncState()
	s.syncState.Status = "running"
	s.syncState.SyncStartTime = strPtr(isoTime(time.Now()))

	s.catalogMu.Lock()
	previous := s.catalog
	s.catalogMu.Unlock()

	opts := catalogmeta.SyncOptions{
		DataDir:        s.cfg.RuntimeCatalogDir,
		RegistryConfig: s.cfg.AuthfilePath,
		Parallel:       3,
		OCPVersions:    s.syncVersions,
		CatalogTypes:   s.syncCatalogTypes,
		RetryDelay:     s.syncRetryDelay,
		Log:            s.appendSyncLog,
		OnStart: func(catalogType, version string) {
			s.syncMu.Lock()
			s.syncState.CurrentCatalog = strPtr(catalogType + " v" + version)
			s.syncMu.Unlock()
		},
		OnDone: func(string, string, bool) {
			s.syncMu.Lock()
			s.syncState.CompletedCatalogs++
			s.syncMu.Unlock()
		},
	}
	go func() {
		result, err := catalogmeta.Sync(context.Background(), opts)
		s.finishCatalogSync(result, err, previous)
	}()

	writeJSON(w, http.StatusOK, jsonObject{"message": "Catalog sync started", "status": "running"})
}

func (s *Server) finishCatalogSync(result catalogmeta.SyncResult, syncErr error, previous *catalogData) {
	s.syncMu.Lock()
	st := &s.syncState
	st.LastSyncTime = strPtr(isoTime(time.Now()))
	st.SuccessCount, st.FailedCount = result.Successful, result.Failed
	if syncErr != nil {
		st.Status = "failed"
		st.Error = strPtr(syncErr.Error())
		s.syncMu.Unlock()
		log.Printf("Catalog sync failed: %v", syncErr)
		return
	}
	s.syncMu.Unlock()

	s.catalogMu.Lock()
	s.resetCatalogCaches()
	fresh := s.loadCatalogData()
	s.catalogMu.Unlock()

	s.syncMu.Lock()
	if fresh != nil && previous != nil {
		s.syncState.Diff = computeCatalogDiff(previous, fresh)
	}
	s.syncState.Status = "completed"
	s.syncMu.Unlock()
	log.Println("Catalog sync completed successfully. Cache reloaded.")
}

func (s *Server) hasRuntimeSyncData() bool {
	f, err := os.Open(filepath.Join(s.cfg.RuntimeCatalogDir, "catalog-index.json"))
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func (s *Server) handleCatalogSyncStatus(w http.ResponseWriter, _ *http.Request) {
	hasData := s.hasRuntimeSyncData()
	s.syncMu.Lock()
	st := s.syncState
	st.Logs = append([]string{}, st.Logs...)
	st.Diff = append([]catalogSyncDiffEntry{}, st.Diff...)
	s.syncMu.Unlock()
	st.HasRuntimeSyncData = hasData
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleCatalogSyncDelete(w http.ResponseWriter, _ *http.Request) {
	if !s.hasRuntimeSyncData() {
		writeJSON(w, http.StatusOK, jsonObject{"message": "No synced catalog data to clear"})
		return
	}
	entries, err := os.ReadDir(s.cfg.RuntimeCatalogDir)
	if err == nil {
		for _, e := range entries {
			if err = os.RemoveAll(filepath.Join(s.cfg.RuntimeCatalogDir, e.Name())); err != nil {
				break
			}
		}
	}
	if err != nil {
		log.Printf("Error clearing synced catalog data: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to clear synced catalog data"))
		return
	}

	s.catalogMu.Lock()
	s.resetCatalogCaches()
	s.catalogMu.Unlock()

	s.syncMu.Lock()
	s.syncState = s.newCatalogSyncState()
	s.syncMu.Unlock()

	log.Println("Runtime catalog data cleared. Will fall back to built-in data on next load.")
	writeJSON(w, http.StatusOK, jsonObject{"message": "Synced catalog data cleared. Falling back to built-in catalog data."})
}
