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

func cleanupExpectedCursorAgentSymlink(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("cursor agent symlink path %q: %w", path, err)
	}
	if rel != filepath.Join(".local", "bin", "agent") {
		return false, nil
	}
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("cursor agent symlink metadata %q: %w", path, err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	if err := validateCursorPrivateTreeOwnership(path, linkInfo); err != nil {
		return false, err
	}
	target, err := os.Readlink(path)
	if err != nil {
		return false, fmt.Errorf("read cursor agent symlink %q: %w", path, err)
	}
	if !filepath.IsAbs(target) || !isExpectedCursorRuntimeExecutable(root, target) {
		return false, nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("resolve cursor home %q: %w", root, err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return false, nil
	}
	targetRel, err := filepath.Rel(root, target)
	if err != nil || resolvedTarget != filepath.Join(resolvedRoot, targetRel) {
		return false, nil
	}
	contained, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return false, nil
	}
	targetInfo, err := os.Lstat(target)
	if err != nil || !targetInfo.Mode().IsRegular() || targetInfo.Mode().Perm() != 0o700 {
		return false, nil
	}
	if err := validateCursorPrivateTreeOwnership(target, targetInfo); err != nil {
		return false, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("cursor agent symlink recheck %q: %w", path, err)
	}
	currentTarget, err := os.Readlink(path)
	if err != nil {
		return false, fmt.Errorf("read cursor agent symlink during recheck %q: %w", path, err)
	}
	if !os.SameFile(linkInfo, current) || current.Mode()&os.ModeSymlink == 0 || currentTarget != target {
		return false, fmt.Errorf("cursor agent symlink %q changed during cleanup", path)
	}
	if err := syscall.Unlink(path); err != nil {
		return false, fmt.Errorf("unlink expected cursor agent symlink %q: %w", path, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err == nil {
			return false, fmt.Errorf("expected cursor agent symlink %q still exists after cleanup", path)
		}
		return false, fmt.Errorf("verify expected cursor agent symlink cleanup %q: %w", path, err)
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
