//go:build !windows

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// cursorAttestedRuntimeExecutableSHA256 is the metadata-only attestation for
// Cursor's generated home/.local/bin/cursor launcher observed with the
// supported CLI compatibility lane. Runtime errors never expose this value.
const cursorAttestedRuntimeExecutableSHA256 = "548f4c6948758b82edadb49eee3fc0e5223e967412172a64bc0459fe807f9ddb"

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

func cleanupExpectedCursorRuntimeExecutable(root, path string, info os.FileInfo, trustedExecutable string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, fmt.Errorf("cursor runtime executable path %q: %w", path, err)
	}
	if rel != filepath.Join(".local", "bin", "cursor") {
		return false, nil
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		return false, nil
	}
	if err := validateCursorPrivateTreeOwnership(path, info); err != nil {
		return false, err
	}
	actualHash, err := cursorFileSHA256(path)
	if err != nil {
		return false, fmt.Errorf("hash cursor runtime executable %q: %w", path, err)
	}
	attested := hex.EncodeToString(actualHash[:]) == cursorAttestedRuntimeExecutableSHA256
	if trustedExecutable != "" {
		trustedInfo, statErr := os.Lstat(trustedExecutable)
		if statErr != nil || !trustedInfo.Mode().IsRegular() {
			return false, fmt.Errorf("cursor trusted executable %q must be a real regular file", trustedExecutable)
		}
		trustedHash, hashErr := cursorFileSHA256(trustedExecutable)
		if hashErr != nil {
			return false, fmt.Errorf("hash cursor trusted executable %q: %w", trustedExecutable, hashErr)
		}
		attested = attested || actualHash == trustedHash
	}
	if !attested {
		return false, nil
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("cursor runtime executable recheck %q: %w", path, err)
	}
	currentHash, err := cursorFileSHA256(path)
	if err != nil {
		return false, fmt.Errorf("rehash cursor runtime executable %q: %w", path, err)
	}
	if !os.SameFile(info, current) || !current.Mode().IsRegular() || current.Mode().Perm() != 0o755 || currentHash != actualHash {
		return false, fmt.Errorf("cursor runtime executable %q changed during cleanup", path)
	}
	if err := syscall.Unlink(path); err != nil {
		return false, fmt.Errorf("unlink expected cursor runtime executable %q: %w", path, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err == nil {
			return false, fmt.Errorf("expected cursor runtime executable %q still exists after cleanup", path)
		}
		return false, fmt.Errorf("verify expected cursor runtime executable cleanup %q: %w", path, err)
	}
	return true, nil
}

func cursorFileSHA256(path string) ([sha256.Size]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return [sha256.Size]byte{}, fmt.Errorf("open returned no file")
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], hash.Sum(nil))
	return sum, nil
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
