//go:build windows

package agent

import "os"

// Windows does not expose Unix uid/link-count semantics through os.FileInfo.
// The recursive no-symlink and private-mode checks remain in force.
func validateCursorPrivateTreeOwnership(_ string, _ os.FileInfo) error { return nil }

func cleanupExpectedCursorWorkerSocket(_, _ string, _ os.FileInfo) (bool, error) {
	return false, nil
}

func cleanupExpectedCursorAgentSymlink(_, _ string) (bool, error) { return false, nil }

func cleanupExpectedCursorRuntimeExecutable(_, _ string, _ os.FileInfo, _ string) (bool, error) {
	return false, nil
}
