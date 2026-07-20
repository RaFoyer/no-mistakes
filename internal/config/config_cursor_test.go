package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestLoadGlobalCursorNativeProvider(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "cursor-profile")
	path := filepath.Join(dir, "config.yaml")
	data := "agent: cursor\ncursor_config_dir: " + profile + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadGlobal(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != types.AgentCursor || cfg.CursorConfigDir != profile {
		t.Fatalf("cursor config = %+v", cfg)
	}
	merged := Merge(cfg, &RepoConfig{})
	if merged.CursorConfigDir != profile || merged.AgentPathFor(types.AgentCursor) != "cursor-agent" {
		t.Fatalf("merged cursor config = %+v", merged)
	}
}

func TestLoadGlobalCursorConfigDirMustBeAbsolute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("agent: cursor\ncursor_config_dir: relative/profile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadGlobal(path); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want absolute-path refusal", err)
	}
}

func TestResolveCursorIsExplicitAndNotAutoDetected(t *testing.T) {
	cfg := &Config{Agent: types.AgentCursor}
	if err := cfg.ResolveAgent(context.Background(), func(bin string) (string, error) {
		if bin != "cursor-agent" {
			t.Fatalf("lookPath(%q), want cursor-agent", bin)
		}
		return "/bin/cursor-agent", nil
	}); err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != types.AgentCursor {
		t.Fatalf("resolved agent = %q", cfg.Agent)
	}

	auto := &Config{Agent: types.AgentAuto}
	if err := auto.ResolveAgent(context.Background(), func(bin string) (string, error) {
		if bin == "cursor-agent" {
			t.Fatal("auto detection must not probe Cursor")
		}
		if bin == "codex" {
			return "/bin/codex", nil
		}
		return "", os.ErrNotExist
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorManagedArgsCannotOverrideSecurityContract(t *testing.T) {
	for _, arg := range []string{"--model", "--force", "--sandbox", "--worktree", "--plugin-dir", "--approve-mcps", "--api-key", "--header"} {
		err := validateAgentArgsOverride(map[string][]string{"cursor": {arg}})
		if err == nil {
			t.Fatalf("managed Cursor arg %q was accepted", arg)
		}
	}
}
