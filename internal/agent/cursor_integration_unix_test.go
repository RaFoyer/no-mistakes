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
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + cursorSupportedVersion + "'; exit 0; fi\n" + body
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
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t)}
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
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t)}
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
	a := &cursorAgent{bin: bin, configDir: privateCursorProfile(t)}
	_, err := a.runOnce(context.Background(), RunOpts{Prompt: "review", CWD: t.TempDir(), Purpose: "review", Routing: routing.Decide(routing.Input{Harness: "cursor", Purpose: "review"})})
	if err == nil || !strings.Contains(err.Error(), "cursor parse stream") {
		t.Fatalf("error = %v", err)
	}
}
