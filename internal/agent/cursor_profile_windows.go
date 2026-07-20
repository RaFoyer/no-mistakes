//go:build windows

package agent

import "os"

// Windows does not expose Unix uid/link-count semantics through os.FileInfo.
// The recursive no-symlink and private-mode checks remain in force.
func validateCursorProfileOwnership(_ string, _ os.FileInfo) error { return nil }
