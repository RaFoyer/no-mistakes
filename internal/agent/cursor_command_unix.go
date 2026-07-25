//go:build unix

package agent

import (
	"context"
	"os/exec"
)

const cursorPrivateLaunchScript = `umask 077; exec "$@"`

// newCursorCommand applies a private creation mask before replacing the shell
// with the pinned Cursor executable. The executable and every argument are
// positional parameters, never interpolated into shell source, so spaces and
// metacharacters remain literal. exec preserves the process PID used for
// lifecycle reporting and process-group cancellation.
func newCursorCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	shellArgs := make([]string, 0, len(args)+4)
	shellArgs = append(shellArgs, "-c", cursorPrivateLaunchScript, "no-mistakes-cursor", bin)
	shellArgs = append(shellArgs, args...)
	return exec.CommandContext(ctx, "/bin/sh", shellArgs...)
}
