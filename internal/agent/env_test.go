package agent

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

type environmentCaptureAgent struct {
	env map[string]string
}

func (a *environmentCaptureAgent) Name() string { return "claude" }

func (a *environmentCaptureAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	a.env = resolveAgentEnv(nonCodexProcessEnv(ctx, opts.CWD))
	return &Result{}, nil
}

func (a *environmentCaptureAgent) Close() error { return nil }

func resolveAgentEnv(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		m[k] = v
	}
	return m
}

func TestGitSafeEnv_DisablesInteractiveGit(t *testing.T) {
	t.Setenv("GIT_EDITOR", "vim")

	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))

	if resolved["GIT_EDITOR"] != "true" {
		t.Errorf("GIT_EDITOR = %q, want \"true\"", resolved["GIT_EDITOR"])
	}
	if resolved["GIT_SEQUENCE_EDITOR"] != "true" {
		t.Errorf("GIT_SEQUENCE_EDITOR = %q, want \"true\"", resolved["GIT_SEQUENCE_EDITOR"])
	}
	if resolved["GIT_TERMINAL_PROMPT"] != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want \"0\"", resolved["GIT_TERMINAL_PROMPT"])
	}
}

// TestGitSafeEnv_CouplesPWDToWorkdir guards the regression where assigning
// cmd.Env dropped os/exec's automatic PWD=cmd.Dir, making os.Getwd in the agent
// report a symlink-resolved path instead of the worktree path.
func TestGitSafeEnv_CouplesPWDToWorkdir(t *testing.T) {
	t.Setenv("PWD", "/somewhere/else")

	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))

	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		if resolved["PWD"] != "/somewhere/else" {
			t.Errorf("PWD = %q, want ambient PWD on %s", resolved["PWD"], runtime.GOOS)
		}
		return
	}

	if resolved["PWD"] != "/work/dir" {
		t.Errorf("PWD = %q, want \"/work/dir\"", resolved["PWD"])
	}
}

// TestGitSafeEnv_StampsGateRoleMarker locks in the ambient-authority containment
// marker: every spawned gate agent must carry NO_MISTAKES_GATE=1 so a
// cooperating orchestration harness in the target repo can recognize the gate
// agent and refuse to let it drive the fleet. If this regresses, a gate agent
// validating a firstmate-shaped repo becomes indistinguishable from a real
// fleet operator.
func TestGitSafeEnv_StampsGateRoleMarker(t *testing.T) {
	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))
	if resolved[GateRoleEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\"", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
}

// TestGitSafeEnv_GateMarkerWinsOverAmbient guards that a target repo (or a
// confused parent) cannot pre-empt the marker with its own ambient value: the
// stamp is appended last, and exec resolves duplicate keys to the last
// occurrence.
func TestGitSafeEnv_GateMarkerWinsOverAmbient(t *testing.T) {
	t.Setenv(GateRoleEnvVar, "0")
	resolved := resolveAgentEnv(gitSafeEnv("/work/dir"))
	if resolved[GateRoleEnvVar] != "1" {
		t.Errorf("%s = %q, want \"1\" (managed stamp must win over ambient)", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
}

func TestCodexProcessEnvAddsOnlyStateRootAndPreservesManagedPrecedence(t *testing.T) {
	t.Setenv("CODEX_HOME", "/ambient/codex")
	t.Setenv("GIT_EDITOR", "vim")
	t.Setenv("GIT_SEQUENCE_EDITOR", "vim")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv(GateRoleEnvVar, "0")
	t.Setenv("PWD", "/ambient/work")

	resolved := resolveAgentEnv(codexProcessEnv("/gate/worktree", "/private/codex-run", true))
	if resolved["CODEX_HOME"] != "/private/codex-run" {
		t.Fatalf("CODEX_HOME = %q, want selected per-run root", resolved["CODEX_HOME"])
	}
	if resolved["GIT_EDITOR"] != "true" || resolved["GIT_SEQUENCE_EDITOR"] != "true" || resolved["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("git-safe editor/prompt precedence changed: %#v", resolved)
	}
	if resolved[GateRoleEnvVar] != "1" {
		t.Fatalf("%s = %q, want managed marker", GateRoleEnvVar, resolved[GateRoleEnvVar])
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "plan9" && resolved["PWD"] != "/gate/worktree" {
		t.Fatalf("PWD = %q, want gate worktree", resolved["PWD"])
	}
}

func TestNonCodexProcessEnvDropsManagedCodexHomeWithoutChangingGitSafeValues(t *testing.T) {
	t.Setenv("CODEX_HOME", "/private/codex-run")
	t.Setenv("GIT_EDITOR", "vim")
	unmanaged := resolveAgentEnv(nonCodexProcessEnv(context.Background(), "/gate/worktree"))
	if unmanaged["CODEX_HOME"] != "/private/codex-run" {
		t.Fatal("unmanaged adapter environment unexpectedly changed")
	}

	managedCtx := withCodexHomeIsolation(context.Background())
	managed := resolveAgentEnv(nonCodexProcessEnv(managedCtx, "/gate/worktree"))
	if _, ok := managed["CODEX_HOME"]; ok {
		t.Fatal("managed non-Codex subprocess received CODEX_HOME")
	}
	if managed["GIT_EDITOR"] != "true" || managed["GIT_SEQUENCE_EDITOR"] != "true" || managed["GIT_TERMINAL_PROMPT"] != "0" || managed[GateRoleEnvVar] != "1" {
		t.Fatalf("managed non-Codex environment changed git-safe precedence: %#v", managed)
	}
}

func TestManagedCodexProcessEnvNeverSubstitutesAmbientStateRoot(t *testing.T) {
	t.Setenv("CODEX_HOME", "/ambient/must-not-be-recovered")
	resolved := resolveAgentEnv(codexProcessEnv("/gate/worktree", "", true))
	if _, ok := resolved["CODEX_HOME"]; ok {
		t.Fatal("managed Codex process substituted ambient CODEX_HOME for missing per-run custody")
	}
}

func TestWithCodexHomeIsolationKeepsSelectedRootOutOfNonCodexAdapterProcess(t *testing.T) {
	t.Setenv("CODEX_HOME", "/private/codex-run")
	inner := &environmentCaptureAgent{}
	wrapped := WithSteering(WithCodexHomeIsolation(inner))
	if _, err := wrapped.Run(context.Background(), RunOpts{CWD: "/gate/worktree"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := inner.env["CODEX_HOME"]; ok {
		t.Fatal("selected Codex root reached a wrapped non-Codex adapter process")
	}
	if inner.env[GateRoleEnvVar] != "1" || inner.env["GIT_TERMINAL_PROMPT"] != "0" {
		t.Fatalf("wrapper changed managed environment precedence: %#v", inner.env)
	}
}
