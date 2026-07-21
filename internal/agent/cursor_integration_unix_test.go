//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/routing"
)

func TestCursorConfigDirRejectsSpecialFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "profile")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(dir, "credential-pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(dir); err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("special profile entry error = %v, want regular-file refusal", err)
	}
}

func writeCursorFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-agent")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + cursorSupportedVersion + "'; exit 0; fi\nif [ \"$1\" = \"status\" ]; then echo '{\"status\":\"authenticated\",\"isAuthenticated\":true,\"hasAccessToken\":true,\"hasRefreshToken\":true}'; exit 0; fi\n" + body
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateCursorProfile(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "profile")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func privateCursorHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCursorRunCapturesExactLargeStdinWithoutArgvLeak(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "stdin")
	argsCapture := filepath.Join(t.TempDir(), "args")
	t.Setenv("CURSOR_CAPTURE_STDIN", capture)
	t.Setenv("CURSOR_CAPTURE_ARGS", argsCapture)
	bin := writeCursorFixture(t, `
printf '%s\n' "$*" > "$CURSOR_CAPTURE_ARGS"
cat > "$CURSOR_CAPTURE_STDIN"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]},"timestamp_ms":1}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s","usage":{"inputTokens":1,"outputTokens":1}}'
`)
	prompt := "SENSITIVE-LARGE-PROMPT:" + strings.Repeat("x", 1<<20)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	res, err := a.runOnce(context.Background(), RunOpts{Prompt: prompt, CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Fatalf("result = %+v", res)
	}
	stdin, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != prompt {
		t.Fatalf("stdin bytes = %d, want %d", len(stdin), len(prompt))
	}
	args, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "SENSITIVE-LARGE-PROMPT") {
		t.Fatal("prompt leaked into argv")
	}
}

func TestCursorRunCancellationTerminatesHeadlessInvocation(t *testing.T) {
	bin := writeCursorFixture(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}'
sleep 30
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := a.runOnce(ctx, RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("cancellation took %v", time.Since(started))
	}
}

func TestCursorRunSurfacesNonzeroExit(t *testing.T) {
	bin := writeCursorFixture(t, "echo 'provider unavailable' >&2\nexit 7\n")
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err == nil || !strings.Contains(err.Error(), "cursor parse stream") {
		t.Fatalf("error = %v", err)
	}
}

func TestCursorRunAuthenticationPreflightUsesExactIsolatedEnvironment(t *testing.T) {
	home := privateCursorHome(t)
	profile := privateCursorProfile(t)
	t.Setenv("CURSOR_EXPECT_HOME", home)
	t.Setenv("CURSOR_EXPECT_PROFILE", profile)
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
test "$HOME" = "$CURSOR_EXPECT_HOME" || { echo "wrong HOME" >&2; exit 9; }
test "$CURSOR_CONFIG_DIR" = "$CURSOR_EXPECT_PROFILE" || { echo "wrong CURSOR_CONFIG_DIR" >&2; exit 9; }
test "$AGENT_CLI_CREDENTIAL_STORE" = "file" || exit 9
test "$NO_OPEN_BROWSER" = "1" || exit 9
test -z "$CURSOR_API_KEY" || exit 9
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then
  test "$2" = "--format" && test "$3" = "json" || exit 9
  echo '{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true}'
  exit 0
fi
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s","usage":{"inputTokens":1,"outputTokens":1}}'
`)
	a := &cursorAgent{bin: bin, configDir: profile, homeDir: home}
	if _, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorRunUnauthenticatedPreflightNeverStartsModel(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "model-started")
	t.Setenv("CURSOR_MODEL_MARKER", marker)
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then echo '{"status":"unauthenticated","isAuthenticated":false,"hasAccessToken":false,"hasRefreshToken":false,"message":"Not logged in"}'; exit 0; fi
touch "$CURSOR_MODEL_MARKER"
exit 99
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if !IsAuthorizationRequired(err) {
		t.Fatalf("error = %v, want AuthorizationRequired", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("model invocation started before auth passed: %v", statErr)
	}
}

func TestCursorRunRevalidatesPrivateRootsAfterAuthenticationProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "model-started")
	t.Setenv("CURSOR_MODEL_MARKER", marker)
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then
  umask 022
  printf credential > "$HOME/status-created-token"
  echo '{"status":"authenticated","isAuthenticated":true,"hasAccessToken":true,"hasRefreshToken":true}'
  exit 0
fi
touch "$CURSOR_MODEL_MARKER"
exit 99
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err == nil || !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("error = %v, want post-probe private-root refusal", err)
	}
	if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("model invocation started after unsafe status write: %v", statErr)
	}
}

func TestCursorRunMalformedAuthenticationStatusFailsClosed(t *testing.T) {
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then echo '{not-json'; exit 0; fi
exit 99
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err == nil || !strings.Contains(err.Error(), "authentication status") || IsAuthorizationRequired(err) {
		t.Fatalf("error = %v, want fail-closed malformed-status error", err)
	}
}

func TestCursorRunAuthenticationStatusTimeoutIsBounded(t *testing.T) {
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then sleep 30; exit 0; fi
exit 99
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t), statusTimeout: 50 * time.Millisecond}
	started := time.Now()
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err == nil || !strings.Contains(err.Error(), "status probe timed out") {
		t.Fatalf("error = %v, want bounded timeout", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("status timeout took %v", time.Since(started))
	}
}

func TestCursorRunAuthenticationStatusPreservesCallerCancellation(t *testing.T) {
	bin := writeCursorFixtureRaw(t, `#!/bin/sh
if [ "$1" = "--version" ]; then echo '`+cursorSupportedVersion+`'; exit 0; fi
if [ "$1" = "status" ]; then sleep 30; exit 0; fi
exit 99
`)
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.runOnce(ctx, RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want caller deadline", err)
	}
}

func writeCursorFixtureRaw(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-agent")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCursorHomeDirRejectsHardLinksAndSpecialFiles(t *testing.T) {
	home := privateCursorHome(t)
	credential := filepath.Join(home, "credentials.json")
	if err := os.WriteFile(credential, []byte("opaque"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(credential, filepath.Join(home, "credentials-copy.json")); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(home); err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("hard-link error = %v", err)
	}

	home = privateCursorHome(t)
	pipe := filepath.Join(home, "credential-pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(home); err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("special-file error = %v", err)
	}
}
