//go:build !windows

package agent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
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

func TestCursorPrivateCommandPreservesLifecyclePID(t *testing.T) {
	capture := filepath.Join(t.TempDir(), "pid")
	t.Setenv("CURSOR_PID_CAPTURE", capture)
	bin := writeCursorFixture(t, `
printf '%s' "$$" > "$CURSOR_PID_CAPTURE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s","usage":{"inputTokens":1,"outputTokens":1}}'
`)
	var events []LifecycleEvent
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t), homeDir: privateCursorHome(t)}
	_, err := a.runOnce(context.Background(), RunOpts{
		Prompt: "review", CWD: t.TempDir(), Purpose: "review",
		Routing:     routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"}),
		OnLifecycle: func(event LifecycleEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	pidBytes, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	actualPID, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Phase != LifecyclePhaseStart || events[1].Phase != LifecyclePhaseExit {
		t.Fatalf("lifecycle events = %+v, want start and exit", events)
	}
	if events[0].PID != actualPID || events[1].PID != actualPID {
		t.Fatalf("lifecycle PIDs = %d/%d, executed Cursor PID = %d", events[0].PID, events[1].PID, actualPID)
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
phase=model
if [ "$1" = "--version" ]; then phase=version; fi
if [ "$1" = "status" ]; then phase=status; fi
mkdir -p "$HOME/phase-$phase"
printf private > "$CURSOR_CONFIG_DIR/phase-$phase"
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
	for _, phase := range []string{"version", "status", "model"} {
		fileInfo, err := os.Stat(filepath.Join(profile, "phase-"+phase))
		if err != nil {
			t.Fatal(err)
		}
		if fileInfo.Mode().Perm() != 0o600 {
			t.Fatalf("%s phase file mode = %04o, want 0600", phase, fileInfo.Mode().Perm())
		}
		dirInfo, err := os.Stat(filepath.Join(home, "phase-"+phase))
		if err != nil {
			t.Fatal(err)
		}
		if dirInfo.Mode().Perm() != 0o700 {
			t.Fatalf("%s phase directory mode = %04o, want 0700", phase, dirInfo.Mode().Perm())
		}
	}
}

func TestCursorPrivateCommandPreservesLiteralArgv(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fixture with spaces")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "cursor fixture")
	capture := filepath.Join(root, "captured args")
	createdDir := filepath.Join(root, "created state")
	injection := filepath.Join(root, "must-not-exist")
	script := `#!/bin/sh
printf '%s\000' "$@" > "$CURSOR_TEST_CAPTURE"
mkdir "$CURSOR_TEST_CREATED_DIR"
printf private > "$CURSOR_TEST_CREATED_DIR/token"
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	literal := []string{"value with spaces", "$(touch " + injection + ")", "semi;colon", "quote'\"value"}
	cmd := newCursorCommand(context.Background(), bin, literal...)
	cmd.Env = append(os.Environ(), "CURSOR_TEST_CAPTURE="+capture, "CURSOR_TEST_CREATED_DIR="+createdDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private command: %v: %s", err, out)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
	if len(parts) != len(literal) {
		t.Fatalf("literal argv count = %d, want %d (%q)", len(parts), len(literal), data)
	}
	for i, want := range literal {
		if string(parts[i]) != want {
			t.Fatalf("argv[%d] = %q, want %q", i, parts[i], want)
		}
	}
	if _, err := os.Stat(injection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metacharacter argument executed: %v", err)
	}
	for path, want := range map[string]os.FileMode{capture: 0o600, createdDir: 0o700, filepath.Join(createdDir, "token"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
		}
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

func TestCursorHomeDirRemovesExpectedWorkerSocket(t *testing.T) {
	home := privateCursorShortRoot(t, "home")
	socket := createCursorWorkerSocket(t, home, "private-d015159", "worker.sock")

	if err := validateCursorHomeDir(home); err != nil {
		t.Fatalf("validate home with expected worker socket: %v", err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker socket still exists after validated cleanup: %v", err)
	}
	if err := validateCursorHomeDir(home); err != nil {
		t.Fatalf("validate home after worker socket cleanup: %v", err)
	}
}

func TestCursorRunCleansExpectedWorkerSocketCreatedDuringAttempt(t *testing.T) {
	home := privateCursorShortRoot(t, "home")
	profile := privateCursorProfile(t)
	bin := writeCursorFixture(t, `
printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"ok","session_id":"s","usage":{"inputTokens":1,"outputTokens":1}}'
`)
	var socket string
	a := &cursorAgent{bin: bin, configDir: profile, homeDir: home}
	_, err := a.runOnce(context.Background(), RunOpts{
		Prompt: "review", CWD: t.TempDir(), Purpose: "review",
		Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"}),
		OnLifecycle: func(event LifecycleEvent) {
			if event.Phase == LifecyclePhaseStart {
				socket = createCursorWorkerSocket(t, home, "private-d015159", "worker.sock")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if socket == "" {
		t.Fatal("worker socket was not created during the model attempt")
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker socket still exists after model attempt: %v", err)
	}
}

func TestCursorHomeDirRejectsUnexpectedRuntimeSockets(t *testing.T) {
	tests := []struct {
		name       string
		privateDir string
		socketName string
	}{
		{name: "wrong basename", privateDir: "private-d015159", socketName: "other.sock"},
		{name: "unbounded private id", privateDir: "private-" + strings.Repeat("a", 33), socketName: "worker.sock"},
		{name: "invalid private id", privateDir: "private-not_ok", socketName: "worker.sock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := privateCursorShortRoot(t, "home")
			socket := createCursorWorkerSocket(t, home, tt.privateDir, tt.socketName)
			err := validateCursorHomeDir(home)
			if err == nil || !strings.Contains(err.Error(), "regular file or directory") {
				t.Fatalf("unexpected socket error = %v, want special-file refusal", err)
			}
			if _, err := os.Lstat(socket); err != nil {
				t.Fatalf("unexpected socket was removed: %v", err)
			}
		})
	}
}

func TestCursorConfigDirRejectsExpectedWorkerSocketShape(t *testing.T) {
	profile := privateCursorShortRoot(t, "profile")
	socket := createCursorWorkerSocket(t, profile, "private-d015159", "worker.sock")

	err := validateCursorConfigDir(profile)
	if err == nil || !strings.Contains(err.Error(), "regular file or directory") {
		t.Fatalf("profile worker socket error = %v, want special-file refusal", err)
	}
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("profile worker socket was removed: %v", err)
	}
}

func TestCursorHomeDirDoesNotRepairCredentialFileAtWorkerSocketPath(t *testing.T) {
	home := privateCursorHome(t)
	privateDir := filepath.Join(home, ".cursor", "private-d015159")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(privateDir, "worker.sock")
	if err := os.WriteFile(path, []byte("credential-like state"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := validateCursorHomeDir(home)
	if err == nil || !strings.Contains(err.Error(), "require 0600") {
		t.Fatalf("credential-like file error = %v, want private-mode refusal", err)
	}
	if info, statErr := os.Lstat(path); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("credential-like file changed or removed: info=%v err=%v", info, statErr)
	}
}

func createCursorWorkerSocket(t *testing.T, root, privateDir, name string) string {
	t.Helper()
	dir := filepath.Join(root, ".cursor", privateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateCursorShortRoot(t *testing.T, name string) string {
	t.Helper()
	parent, err := os.MkdirTemp("/tmp", "nm-cursor-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	root := filepath.Join(parent, name)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}
