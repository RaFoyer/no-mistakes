package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestParseSkipPushOptions(t *testing.T) {
	got, err := parseSkipPushOptions([]string{
		"ci.skip",
		"no-mistakes.skip=test,lint",
	})
	if err != nil {
		t.Fatalf("parseSkipPushOptions() error = %v", err)
	}
	want := []types.StepName{types.StepTest, types.StepLint}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSkipPushOptions() = %v, want %v", got, want)
	}
}

func TestParseSkipPushOptionsRejectsUnknownStep(t *testing.T) {
	_, err := parseSkipPushOptions([]string{"no-mistakes.skip=test,deploy"})
	if err == nil {
		t.Fatal("expected unknown step to fail")
	}
}

func TestNormalizeNotifyGatePathResolvesLegacyDotGate(t *testing.T) {
	bare := filepath.Join(t.TempDir(), "repo123.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(bare); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(oldDir); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	t.Setenv("PWD", ".")

	got, err := normalizeNotifyGatePath(".")
	if err != nil {
		t.Fatalf("normalizeNotifyGatePath: %v", err)
	}
	if got == "." || !filepath.IsAbs(got) {
		t.Fatalf("normalizeNotifyGatePath(.) = %q, want absolute path", got)
	}
	want, err := filepath.EvalSymlinks(bare)
	if err != nil {
		want = bare
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotResolved = got
	}
	if gotResolved != want {
		t.Fatalf("normalizeNotifyGatePath(.) = %q (resolved %q), want %q", got, gotResolved, want)
	}
}

func TestSelectedGitHubConfigDirPreservesExplicitSelection(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", "/profiles/acos")
	got := selectedGitHubConfigDir()
	if got == nil || *got != "/profiles/acos" {
		t.Fatalf("selectedGitHubConfigDir() = %#v, want explicit path", got)
	}
}

func TestSelectedGitHubConfigDirDistinguishesUnsetFromEmpty(t *testing.T) {
	t.Setenv("GH_CONFIG_DIR", "")
	if got := selectedGitHubConfigDir(); got == nil || *got != "" {
		t.Fatalf("explicit empty selection = %#v, want pointer to empty string", got)
	}
	if err := os.Unsetenv("GH_CONFIG_DIR"); err != nil {
		t.Fatal(err)
	}
	if got := selectedGitHubConfigDir(); got != nil {
		t.Fatalf("unset selection = %#v, want nil", got)
	}
}

func TestSelectedCodexStateRootDistinguishesUnsetFromEmpty(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	if got := selectedCodexStateRoot(); got == nil || *got != "" {
		t.Fatalf("explicit empty selection = %#v, want pointer to empty string", got)
	}
	if err := os.Unsetenv("CODEX_HOME"); err != nil {
		t.Fatal(err)
	}
	if got := selectedCodexStateRoot(); got != nil {
		t.Fatalf("unset selection = %#v, want nil", got)
	}
}

func TestFormatSkipPushOptions(t *testing.T) {
	got := formatSkipPushOptions([]types.StepName{types.StepTest, types.StepLint})
	want := []string{"no-mistakes.skip=test,lint"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("formatSkipPushOptions() = %v, want %v", got, want)
	}
}

func TestIntentPushOptionRoundTrip(t *testing.T) {
	// Multi-line, comma- and colon-bearing intent must survive the
	// line-oriented push-option transport intact.
	intent := "add retry to the uploader\n\nwhy: flaky network, commas, colons: ok"
	opt := formatIntentPushOption(intent)
	if opt == "" {
		t.Fatal("formatIntentPushOption returned empty for a non-empty intent")
	}
	got, err := parseIntentPushOptions([]string{"no-mistakes.skip=test", opt})
	if err != nil {
		t.Fatalf("parseIntentPushOptions() error = %v", err)
	}
	if got != intent {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, intent)
	}
}

func TestFormatIntentPushOptionEmpty(t *testing.T) {
	if got := formatIntentPushOption("   "); got != "" {
		t.Fatalf("formatIntentPushOption(blank) = %q, want empty", got)
	}
}

func TestParseIntentPushOptionsNone(t *testing.T) {
	got, err := parseIntentPushOptions([]string{"no-mistakes.skip=test", "ci.skip"})
	if err != nil {
		t.Fatalf("parseIntentPushOptions() error = %v", err)
	}
	if got != "" {
		t.Fatalf("parseIntentPushOptions(no intent) = %q, want empty", got)
	}
}

func TestCodexStateRootPushOptionRoundTripIsValueOpaque(t *testing.T) {
	root := "/private/selected/codex-state"
	opt := formatCodexStateRootPushOption(&root)
	if opt == "" {
		t.Fatal("formatCodexStateRootPushOption returned empty for an explicit selection")
	}
	if strings.Contains(opt, root) {
		t.Fatal("push option exposed the selected state-root value")
	}
	got, err := parseCodexStateRootPushOptions([]string{"no-mistakes.skip=test", opt})
	if err != nil {
		t.Fatalf("parseCodexStateRootPushOptions() error = %v", err)
	}
	if got == nil || *got != root {
		t.Fatalf("round-trip selection = %#v, want %q", got, root)
	}
}

func TestCodexStateRootPushOptionPreservesExplicitEmptyAndAbsent(t *testing.T) {
	empty := ""
	opt := formatCodexStateRootPushOption(&empty)
	got, err := parseCodexStateRootPushOptions([]string{opt})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != "" {
		t.Fatalf("explicit empty selection = %#v, want pointer to empty string", got)
	}
	got, err = parseCodexStateRootPushOptions([]string{"no-mistakes.skip=test"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("absent selection = %#v, want nil", got)
	}
}

func TestParseCodexStateRootPushOptionFailsValueSafely(t *testing.T) {
	got, err := parseCodexStateRootPushOptions([]string{codexStateRootPushOptionPrefix + "%%%"})
	if err == nil || got != nil {
		t.Fatalf("malformed selection = %#v, %v; want nil error", got, err)
	}
	if err.Error() != codexStateRootUnavailable {
		t.Fatalf("malformed selection error = %q, want constant value-safe error", err)
	}
}
