//go:build !unix

package agent

import (
	"context"
	"os/exec"
)

// POSIX creation masks do not apply on this platform. Keep the direct command
// path while the recursive Cursor private-tree validation remains in force.
func newCursorCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}
