package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Operation records are stored as JSON objects and updated by merging fields,
// so unknown fields written by other versions are preserved.
type operation = jsonObject

var operationIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (s *Server) operationPath(id string) string {
	return filepath.Join(s.cfg.OperationsDir, id+".json")
}

func (s *Server) logPath(id string) string {
	return filepath.Join(s.cfg.LogsDir, id+".log")
}

func parseTime(v any) time.Time {
	str, _ := v.(string)
	t, _ := time.Parse(time.RFC3339Nano, str)
	return t
}

func (s *Server) getOperations() []operation {
	entries, err := os.ReadDir(s.cfg.OperationsDir)
	if err != nil {
		log.Printf("Error reading operations: %v", err)
		return []operation{}
	}
	ops := []operation{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var op operation
		if err := readJSONFile(filepath.Join(s.cfg.OperationsDir, e.Name()), &op); err != nil || op == nil {
			log.Printf("Skipping corrupt operation file %s: %v", e.Name(), err)
			continue
		}
		ops = append(ops, op)
	}
	sort.SliceStable(ops, func(i, j int) bool {
		return parseTime(ops[i]["startedAt"]).After(parseTime(ops[j]["startedAt"]))
	})
	return ops
}

func atomicWriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Server) getOperation(id string) (operation, error) {
	var op operation
	if err := readJSONFile(s.operationPath(id), &op); err != nil {
		return nil, err
	}
	return op, nil
}

// updateOperation merges updates into a stored operation. It returns nil
// without error when the record is missing or corrupt.
func (s *Server) updateOperation(id string, updates operation) (operation, error) {
	s.opsMu.Lock()
	defer s.opsMu.Unlock()
	op, err := s.getOperation(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) || (err == nil && op == nil) {
		log.Printf("Corrupt operation file %s.json, skipping update", id)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for k, v := range updates {
		op[k] = v
	}
	return op, atomicWriteJSON(s.operationPath(id), op)
}

func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	counts := map[string]int{}
	ops := s.getOperations()
	for _, op := range ops {
		status, _ := op["status"].(string)
		counts[status]++
	}
	writeJSON(w, http.StatusOK, jsonObject{
		"totalOperations":      len(ops),
		"successfulOperations": counts["success"],
		"failedOperations":     counts["failed"],
		"runningOperations":    counts["running"],
	})
}

func (s *Server) handleOperationsList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.getOperations())
}

func (s *Server) handleOperationsRecent(w http.ResponseWriter, _ *http.Request) {
	ops := s.getOperations()
	if len(ops) > 10 {
		ops = ops[:10]
	}
	writeJSON(w, http.StatusOK, ops)
}

var (
	minutesSecondsRe = regexp.MustCompile(`^(?:(\d+)m)?(\d+)s$`)
	minutesOnlyRe    = regexp.MustCompile(`^(\d+)m$`)
)

// parseDurationSeconds accepts the oc-mirror duration forms "30s", "10m" and "10m30s".
func parseDurationSeconds(value string) (int, bool) {
	if m := minutesSecondsRe.FindStringSubmatch(value); m != nil {
		minutes, _ := strconv.Atoi(m[1])
		seconds, _ := strconv.Atoi(m[2])
		return minutes*60 + seconds, true
	}
	if m := minutesOnlyRe.FindStringSubmatch(value); m != nil {
		minutes, _ := strconv.Atoi(m[1])
		return minutes * 60, true
	}
	return 0, false
}

const maxSafeInteger = 1<<53 - 1

// buildOptionalFlagArgs validates the optionalFlags request object and converts it to oc-mirror arguments.
func buildOptionalFlagArgs(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	flags, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("optionalFlags must be an object")
	}
	for key := range flags {
		switch key {
		case "removeSignatures", "imageTimeout", "retryDelay", "retryTimes":
		default:
			return nil, fmt.Errorf("Unknown optional flag: %s", key)
		}
	}

	args := []string{}
	if v := flags["removeSignatures"]; v != nil {
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("optionalFlags.removeSignatures must be a boolean")
		}
		if b {
			args = append(args, "--remove-signatures")
		}
	}
	if v := flags["imageTimeout"]; v != nil {
		str, ok := v.(string)
		if !ok {
			return nil, errors.New(`optionalFlags.imageTimeout must be a duration string (e.g. "10m", "600s", "10m30s")`)
		}
		seconds, valid := parseDurationSeconds(str)
		if !valid {
			return nil, errors.New(`optionalFlags.imageTimeout must match format like "10m", "600s", or "10m30s"`)
		}
		if seconds <= 0 {
			return nil, errors.New("optionalFlags.imageTimeout must be greater than 0")
		}
		args = append(args, "--image-timeout", str)
	}
	if v := flags["retryDelay"]; v != nil {
		str, ok := v.(string)
		if !ok {
			return nil, errors.New(`optionalFlags.retryDelay must be a duration string (e.g. "30s", "1m", "1m30s")`)
		}
		if _, valid := parseDurationSeconds(str); !valid {
			return nil, errors.New(`optionalFlags.retryDelay must match format like "30s", "1m", or "1m30s"`)
		}
		args = append(args, "--retry-delay", str)
	}
	if v := flags["retryTimes"]; v != nil {
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || n < 0 || n > maxSafeInteger {
			return nil, errors.New("optionalFlags.retryTimes must be a non-negative integer")
		}
		args = append(args, "--retry-times", strconv.FormatInt(int64(n), 10))
	}
	return args, nil
}

func errnoCode(err error) any {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errnoNames[errno]
	}
	return nil
}

var errnoNames = map[syscall.Errno]string{
	syscall.EACCES: "EACCES", syscall.EPERM: "EPERM", syscall.ENOENT: "ENOENT",
	syscall.EROFS: "EROFS", syscall.ENOSPC: "ENOSPC", syscall.ENOTDIR: "ENOTDIR",
}

func checkWritable(dir string) error {
	testFile := filepath.Join(dir, ".test-write")
	if err := os.WriteFile(testFile, []byte("test"), 0o644); err != nil {
		return err
	}
	return os.Remove(testFile)
}

// outputBuffer collects process output for the operation record while also writing it to the log file.
type outputBuffer struct {
	mu  sync.Mutex
	log io.Writer
	buf strings.Builder
}

func (o *outputBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf.Write(p)
	if o.log != nil {
		_, _ = o.log.Write(p)
	}
	return len(p), nil
}

func (o *outputBuffer) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

var (
	errorLineRe       = regexp.MustCompile(`(?i)\[error\]|\berror:`)
	logTimestampRe    = regexp.MustCompile(`^\d{4}/\d{2}/\d{2}\s+\d{2}:\d{2}:\d{2}\s*`)
	ansiRe            = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	errorPrefixRe     = regexp.MustCompile(`(?i)^\s*\[ERROR\]\s*`)
	leadingColonRe    = regexp.MustCompile(`^:\s*`)
	containsErrorText = func(logs string) bool {
		lower := strings.ToLower(logs)
		return strings.Contains(lower, "[error]") || strings.Contains(lower, "error:")
	}
)

func extractErrorMessage(logs string, exitCode int) string {
	for _, line := range strings.Split(logs, "\n") {
		if errorLineRe.MatchString(line) {
			line = logTimestampRe.ReplaceAllString(line, "")
			line = ansiRe.ReplaceAllString(line, "")
			line = errorPrefixRe.ReplaceAllString(line, "")
			line = leadingColonRe.ReplaceAllString(line, "")
			return strings.TrimSpace(line)
		}
	}
	return fmt.Sprintf("Process exited with code %d", exitCode)
}

func (s *Server) handleOperationStart(w http.ResponseWriter, r *http.Request) {
	_, body, ok := readBody(w, r)
	if !ok {
		return
	}
	configFile, _ := body["configFile"].(string)
	if configFile == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("configFile is required"))
		return
	}
	if hasPathChars(configFile) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid filename"))
		return
	}
	configPath := filepath.Join(s.cfg.ConfigsDir, configFile)

	additionalArgs, err := buildOptionalFlagArgs(body["optionalFlags"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	if _, err := os.Stat(configPath); err != nil {
		writeJSON(w, http.StatusNotFound, errorBody("Configuration file not found"))
		return
	}

	subdir := "default"
	if raw, _ := body["mirrorDestinationSubdir"].(string); strings.TrimSpace(raw) != "" {
		input := strings.TrimSpace(raw)
		if hasPathChars(input) {
			writeJSON(w, http.StatusBadRequest, jsonObject{
				"error":    "Subdirectory name cannot contain path separators or traversal characters",
				"provided": input,
				"help":     `Use a simple name like "odf" or "production" (no slashes or special characters)`,
			})
			return
		}
		if !simpleNameRe.MatchString(input) {
			writeJSON(w, http.StatusBadRequest, jsonObject{
				"error":    "Subdirectory name contains invalid characters",
				"provided": input,
				"help":     "Use only letters, numbers, dashes (-), and underscores (_)",
			})
			return
		}
		subdir = input
	}

	base := s.cfg.MirrorBaseDir
	if err := os.MkdirAll(base, 0o777); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonObject{
			"error": "Cannot access base mirror directory", "path": base, "details": err.Error(), "code": errnoCode(err),
		})
		return
	}
	if err := checkWritable(base); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonObject{
			"error": "Base mirror directory is not writable", "path": base, "details": err.Error(), "code": errnoCode(err),
		})
		return
	}

	mirrorPath := filepath.Join(base, subdir)
	if err := os.MkdirAll(mirrorPath, 0o775); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonObject{
			"error": "Cannot create or access mirror destination directory", "path": mirrorPath,
			"subdirectory": subdir, "details": err.Error(), "code": errnoCode(err),
		})
		return
	}
	if err := checkWritable(mirrorPath); err != nil {
		writeJSON(w, http.StatusInternalServerError, jsonObject{
			"error": "Mirror destination directory exists but is not writable", "path": mirrorPath,
			"subdirectory": subdir, "details": err.Error(), "code": errnoCode(err),
			"help": "The directory exists but the container cannot write to it. Check permissions on the host.",
		})
		return
	}

	id := newUUID()
	startedAt := time.Now()
	op := operation{
		"id":                id,
		"name":              "Mirror Operation " + id[:8],
		"configFile":        configFile,
		"mirrorDestination": mirrorPath,
		"status":            "running",
		"startedAt":         isoTime(startedAt),
		"logs":              []string{},
	}
	if err := atomicWriteJSON(s.operationPath(id), op); err != nil {
		log.Printf("Error saving operation %s: %v", id, err)
		writeJSON(w, http.StatusInternalServerError, jsonObject{"error": "Failed to create operation record", "details": err.Error()})
		return
	}

	logFile, err := os.Create(s.logPath(id))
	if err != nil {
		log.Printf("Error creating log file for %s: %v", id, err)
	}
	output := &outputBuffer{}
	if logFile != nil {
		output.log = logFile
	}

	mirrorURL := (&url.URL{Scheme: "file", Path: mirrorPath}).String()
	args := append([]string{
		"--v2",
		"--config", configPath,
		"--dest-tls-verify=false",
		"--src-tls-verify=false",
		"--cache-dir", s.cfg.CacheDir,
		"--authfile", s.cfg.AuthfilePath,
	}, additionalArgs...)
	args = append(args, mirrorURL)

	cmd := exec.Command("oc-mirror", args...)
	cmd.Dir = s.cfg.AppRootDir
	cmd.Stdout = output
	cmd.Stderr = output

	finish := func(updates operation) {
		if logFile != nil {
			logFile.Close()
		}
		completedAt := time.Now()
		updates["completedAt"] = isoTime(completedAt)
		updates["duration"] = int(completedAt.Sub(startedAt).Seconds())
		if _, err := s.updateOperation(id, updates); err != nil {
			log.Printf("Error updating operation %s: %v", id, err)
		}
	}

	if err := cmd.Start(); err != nil {
		finish(operation{"status": "failed", "errorMessage": err.Error(), "logs": []string{err.Error()}})
		writeJSON(w, http.StatusOK, jsonObject{"message": "Operation started successfully", "operationId": id})
		return
	}

	proc := &runningProcess{proc: cmd.Process, done: make(chan struct{})}
	s.mu.Lock()
	s.running[id] = proc
	s.mu.Unlock()

	go func() {
		_ = cmd.Wait()
		close(proc.done)

		s.mu.Lock()
		delete(s.running, id)
		wasStopped := s.stopped[id]
		delete(s.stopped, id)
		s.mu.Unlock()

		logs := output.String()
		exitCode := cmd.ProcessState.ExitCode()
		status := "success"
		var errorMessage any
		switch {
		case wasStopped:
			status = "stopped"
		case exitCode != 0 || containsErrorText(logs):
			status = "failed"
			errorMessage = extractErrorMessage(logs, exitCode)
		}
		finish(operation{"status": status, "errorMessage": errorMessage, "logs": strings.Split(logs, "\n")})
	}()

	writeJSON(w, http.StatusOK, jsonObject{"message": "Operation started successfully", "operationId": id})
}

// stopGracePeriod is how long a stopped oc-mirror gets to exit after SIGTERM before SIGKILL.
const stopGracePeriod = 5 * time.Second

func (s *Server) handleOperationStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid operation id"))
		return
	}

	s.mu.Lock()
	proc := s.running[id]
	if proc != nil {
		s.stopped[id] = true
		delete(s.running, id)
	}
	s.mu.Unlock()

	if proc != nil {
		if err := proc.proc.Signal(syscall.SIGTERM); err != nil {
			log.Printf("Error stopping process for %s: %v", id, err)
		}
		go func() {
			select {
			case <-proc.done:
			case <-time.After(stopGracePeriod):
				_ = proc.proc.Kill()
			}
		}()
	} else if _, err := s.updateOperation(id, operation{
		"status": "stopped", "completedAt": isoTime(time.Now()), "errorMessage": nil,
	}); err != nil {
		log.Printf("Error stopping operation: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to stop operation"))
		return
	}
	writeJSON(w, http.StatusOK, jsonObject{"message": "Operation stopped successfully"})
}

func (s *Server) handleOperationDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid operation id"))
		return
	}
	_ = os.Remove(s.operationPath(id))
	writeJSON(w, http.StatusOK, jsonObject{"message": "Operation deleted successfully"})
}

func joinLogs(v any) string {
	lines, _ := v.([]any)
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		if str, ok := l.(string); ok {
			parts = append(parts, str)
		}
	}
	return strings.Join(parts, "\n")
}

func (s *Server) handleOperationLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid operation id"))
		return
	}
	op, err := s.getOperation(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to get operation logs"))
		return
	}
	logs := ""
	if b, err := os.ReadFile(s.logPath(id)); err == nil {
		logs = string(b)
	} else {
		logs = joinLogs(op["logs"])
	}
	writeJSON(w, http.StatusOK, jsonObject{"logs": logs})
}

var (
	imagesToCopyRe     = regexp.MustCompile(`📌 images to copy (\d+)`)
	operatorSuccessRe  = regexp.MustCompile(`✓ (\d+) / (\d+) operator images mirrored successfully`)
	collectedCatalogRe = regexp.MustCompile(`Collected catalog ([^\n]+)`)
	releaseCopiedRe    = regexp.MustCompile(`Success copying.*release.*➡️ cache`)
	additionalCopiedRe = regexp.MustCompile(`Success copying.*additional.*➡️ cache`)
	helmCopiedRe       = regexp.MustCompile(`Success copying.*helm.*➡️ cache`)
)

func (s *Server) handleOperationDetails(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusNotFound, errorBody("Operation not found"))
		return
	}
	op, err := s.getOperation(id)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, http.StatusNotFound, errorBody("Operation not found"))
		return
	}
	if err != nil {
		log.Printf("Error getting operation details: %v", err)
		writeJSON(w, http.StatusInternalServerError, errorBody("Failed to get operation details"))
		return
	}

	imagesMirrored, operatorsMirrored := 0, 0
	platformImages, additionalImages, helmCharts := 0, 0, 0
	if _, ok := op["logs"].([]any); ok {
		logs := joinLogs(op["logs"])
		if m := imagesToCopyRe.FindStringSubmatch(logs); m != nil {
			imagesMirrored, _ = strconv.Atoi(m[1])
		}
		if m := operatorSuccessRe.FindStringSubmatch(logs); m != nil {
			operatorsMirrored, _ = strconv.Atoi(m[1])
		}
		if matches := collectedCatalogRe.FindAllString(logs, -1); len(matches) > 0 {
			operatorsMirrored = len(matches)
		}
		if strings.Contains(logs, "🔍 collecting release images") {
			platformImages = len(releaseCopiedRe.FindAllString(logs, -1))
		}
		if strings.Contains(logs, "🔍 collecting additional images") {
			additionalImages = len(additionalCopiedRe.FindAllString(logs, -1))
		}
		if strings.Contains(logs, "🔍 collecting helm images") {
			helmCharts = len(helmCopiedRe.FindAllString(logs, -1))
		}
	}

	writeJSON(w, http.StatusOK, jsonObject{
		"imagesMirrored":    imagesMirrored,
		"operatorsMirrored": operatorsMirrored,
		// Rough estimate: oc-mirror does not report total size.
		"totalSize":        imagesMirrored * 50 * 1024 * 1024,
		"platformImages":   platformImages,
		"additionalImages": additionalImages,
		"helmCharts":       helmCharts,
		"configFile":       op["configFile"],
		"manifestFiles":    []string{"imageContentSourcePolicy.yaml", "catalogSource.yaml", "mapping.txt"},
	})
}

// logStreamInterval is how often the log stream polls the log file.
var logStreamInterval = time.Second

// handleOperationLogStream tails an operation's log file as server-sent events
// and sends a "done" event once the operation is no longer running.
func (s *Server) handleOperationLogStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !operationIDRe.MatchString(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid operation id"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody("Streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var pos int64
	idleTicks := 0
	ticker := time.NewTicker(logStreamInterval)
	defer ticker.Stop()

	for {
		sent := false
		if f, err := os.Open(s.logPath(id)); err == nil {
			if info, err := f.Stat(); err == nil && info.Size() > pos {
				chunk := make([]byte, info.Size()-pos)
				n, _ := f.ReadAt(chunk, pos)
				if n > 0 {
					pos += int64(n)
					data := strings.ReplaceAll(string(chunk[:n]), "\n", "\ndata: ")
					fmt.Fprintf(w, "data: %s\n\n", data)
					flusher.Flush()
					sent = true
				}
			}
			f.Close()
		}
		if sent {
			idleTicks = 0
		} else {
			idleTicks++
		}

		s.mu.Lock()
		_, running := s.running[id]
		s.mu.Unlock()
		if !running && idleTicks >= 2 {
			status := "unknown"
			if op, err := s.getOperation(id); err == nil {
				if st, ok := op["status"].(string); ok && st != "" {
					status = st
				}
			}
			done, _ := json.Marshal(jsonObject{"id": id, "status": status})
			fmt.Fprintf(w, "event: done\ndata: %s\n\n", done)
			flusher.Flush()
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
