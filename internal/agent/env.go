package agent

import (
	"context"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
)

// GateRoleEnvVar is exported into every spawned gate agent's environment as an
// unspoofable-from-outside marker that the process is a no-mistakes gate agent
// (a review/fix/document/test/lint/rebase/pr/ci invocation), NOT a fleet
// operator. Its purpose is containment: when the target repository is itself an
// agent-orchestration harness (for example firstmate), the target's project
// agent-instruction file can otherwise convince the gate agent it is the fleet
// captain and drive it to spawn a crew and reset the shared branch it is
// validating (see the ambient-authority incident). A cooperating harness reads
// this marker and its fleet-lifecycle entrypoints fail closed. It is deliberately
// coarse (`=1`): presence is the whole signal.
const GateRoleEnvVar = "NO_MISTAKES_GATE"

// gitSafeEnv returns the environment for a spawned agent subprocess with git
// forced into non-interactive mode. Agents shell out to git directly (for
// example `git rebase --continue` during conflict resolution), which would
// otherwise open $EDITOR and hang in the headless subprocess until the agent
// times out.
//
// It also stamps GateRoleEnvVar so a cooperating orchestration harness in the
// target repo can recognize the gate agent and refuse to let it act as a fleet
// operator. Appended last so it wins over any ambient value.
//
// dir must be the value assigned to cmd.Dir so PWD stays coupled to the working
// directory; see git.NonInteractiveEnv for why this matters.
func gitSafeEnv(dir string) []string {
	return append(git.NonInteractiveEnv(dir), GateRoleEnvVar+"=1")
}

// codexProcessEnv isolates managed pipeline invocations from the daemon's
// ambient CODEX_HOME, then adds only the selected per-run root. Unmanaged
// callers retain the historical inherited environment.
func codexProcessEnv(dir, stateRoot string, managed bool) []string {
	env := gitSafeEnv(dir)
	if managed {
		env = withoutEnvKey(env, "CODEX_HOME")
	}
	if stateRoot == "" {
		return env
	}
	return append(env, "CODEX_HOME="+stateRoot)
}

type codexHomeIsolationContextKey struct{}

func withCodexHomeIsolation(ctx context.Context) context.Context {
	return context.WithValue(ctx, codexHomeIsolationContextKey{}, true)
}

func codexHomeIsolationEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(codexHomeIsolationContextKey{}).(bool)
	return enabled
}

// nonCodexProcessEnv removes CODEX_HOME only for daemon-managed pipeline
// adapters, preventing the selected Codex root from reaching another tool.
func nonCodexProcessEnv(ctx context.Context, dir string) []string {
	env := gitSafeEnv(dir)
	if !codexHomeIsolationEnabled(ctx) {
		return env
	}
	return withoutEnvKey(env, "CODEX_HOME")
}

func withoutEnvKey(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
