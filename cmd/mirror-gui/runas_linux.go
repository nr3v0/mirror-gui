package main

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/nr3v0/mirror-gui/internal/server"
)

// dropPrivileges replaces the former container entrypoint script. When the
// process starts as root and MIRROR_GUI_RUN_AS is set to "uid:gid" (the
// container image sets it), it creates the data directories, hands them and
// the pull secret to that user (host bind mounts are often root-owned), and
// then switches to that user before serving.
func dropPrivileges(cfg server.Config) error {
	spec := os.Getenv("MIRROR_GUI_RUN_AS")
	if spec == "" || os.Geteuid() != 0 {
		return nil
	}
	uidText, gidText, ok := strings.Cut(spec, ":")
	uid, err1 := strconv.Atoi(uidText)
	gid, err2 := strconv.Atoi(gidText)
	if !ok || err1 != nil || err2 != nil {
		return fmt.Errorf("MIRROR_GUI_RUN_AS must be uid:gid, got %q", spec)
	}

	for _, dir := range []string{
		cfg.ConfigsDir, cfg.OperationsDir, cfg.LogsDir, cfg.CacheDir, cfg.DefaultMirrorDir, cfg.RuntimeCatalogDir,
	} {
		if err := os.MkdirAll(dir, 0o775); err != nil {
			return err
		}
	}
	// Give the app user and the root group read/write access to all data.
	err := filepath.WalkDir(cfg.StorageDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return os.Lchown(path, uid, gid)
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() | 0o660
		if d.IsDir() {
			mode = 0o775
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return fmt.Errorf("preparing %s: %w", cfg.StorageDir, err)
	}
	if _, err := os.Stat(cfg.AuthfilePath); err == nil {
		if err := os.Chown(cfg.AuthfilePath, uid, gid); err != nil {
			return err
		}
		if err := os.Chmod(cfg.AuthfilePath, 0o664); err != nil {
			return err
		}
	}

	if err := syscall.Setgroups([]int{gid}); err != nil {
		return err
	}
	if err := syscall.Setgid(gid); err != nil {
		return err
	}
	if err := syscall.Setuid(uid); err != nil {
		return err
	}

	if err := syscall.Access(cfg.ConfigsDir, 0x2); err != nil {
		return fmt.Errorf("%s is not writable by uid %d; on the host run: sudo chown -R %d:%d data/ && sudo chmod -R 775 data/",
			cfg.ConfigsDir, uid, uid, gid)
	}
	log.Printf("Running as uid %d, gid %d", uid, gid)
	return nil
}
