//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validateCursorPrivateTreeOwnership(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cursor private tree entry %q has unavailable ownership metadata", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("cursor private tree entry %q is foreign-owned", path)
	}
	if info.Mode().IsRegular() && stat.Nlink != 1 {
		return fmt.Errorf("cursor private tree file %q is hard-linked", path)
	}
	return nil
}

// cleanupExpectedCursorWorkerSocket unlinks only the pinned Cursor runtime's
// transient home/.cursor/private-<hex>/worker.sock Unix socket.
func cleanupExpectedCursorWorkerSocket(root, path string, info os.FileInfo) (bool, error) {
	if info.Mode()&os.ModeSocket == 0 {
		return false, nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("cursor worker socket path %q: %w", path, err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != ".cursor" || parts[2] != "worker.sock" || !validCursorPrivateRuntimeDir(parts[1]) {
		return false, nil
	}
	if err := validateCursorPrivateTreeOwnership(path, info); err != nil {
		return false, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("cursor worker socket recheck %q: %w", path, err)
	}
	if !os.SameFile(info, current) || current.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("cursor worker socket %q changed during cleanup", path)
	}
	if err := syscall.Unlink(path); err != nil {
		return false, fmt.Errorf("unlink expected cursor worker socket %q: %w", path, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err == nil {
			return false, fmt.Errorf("expected cursor worker socket %q still exists after cleanup", path)
		}
		return false, fmt.Errorf("verify expected cursor worker socket cleanup %q: %w", path, err)
	}
	return true, nil
}

func validCursorPrivateRuntimeDir(name string) bool {
	const prefix = "private-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	id := strings.TrimPrefix(name, prefix)
	if len(id) == 0 || len(id) > 32 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
