package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/routing"
)

func TestCursorBuildArgsKeepsPromptAndSchemaOutOfArgv(t *testing.T) {
	a := &cursorAgent{extraArgs: []string{"--header", "X-Test: safe"}}
	args := a.buildArgs(cursorModelMedium, false)
	got := strings.Join(args, " ")
	for _, want := range []string{
		"--print", "--output-format stream-json", "--stream-partial-output",
		"--model " + cursorModelMedium, "--sandbox enabled", "--mode ask",
		"--trust", "--skip-worktree-setup",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("args %q missing %q", got, want)
		}
	}
	for _, forbidden := range []string{"--force", "--worktree", "--approve-mcps", "prompt", "schema"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("args %q unexpectedly contain %q", got, forbidden)
		}
	}
}

func TestCursorBuildArgsForceOnlyForFix(t *testing.T) {
	a := &cursorAgent{}
	if got := strings.Join(a.buildArgs(cursorModelHigh, true), " "); !strings.Contains(got, "--force") || strings.Contains(got, "--mode ask") {
		t.Fatalf("fix args = %q", got)
	}
}

func TestCursorMutatingPipelinePurposesRequireFixPermissions(t *testing.T) {
	for _, purpose := range []string{"review-fix", "test-fix", "ci-fix", "document", "housekeeping", "lint", "test-evidence"} {
		if !cursorFixPurpose(purpose) {
			t.Errorf("purpose %q must be mutating", purpose)
		}
	}
	for _, purpose := range []string{"review", "review-confirmation", "intent", "pr-summary"} {
		if cursorFixPurpose(purpose) {
			t.Errorf("purpose %q must be read-only", purpose)
		}
	}
}

func TestParseCursorEventsSuccess(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"session-1","model":"Cursor Grok 4.5 Medium","permissionMode":"default","future":"ignored"}`,
		`{"type":"thinking","subtype":"delta","text":"private reasoning ignored","session_id":"session-1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"{\"findings\":"}]},"session_id":"session-1","timestamp_ms":1}`,
		`{"type":"tool","subtype":"started","tool_call":{"type":"read","path":"secret"},"session_id":"session-1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"[]}"}]},"session_id":"session-1","timestamp_ms":2}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"{\"findings\":[]}"}]},"session_id":"session-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"{\"findings\":[]}","session_id":"session-1","usage":{"inputTokens":10,"outputTokens":3,"cacheReadTokens":2,"cacheWriteTokens":1}}`,
	}, "\n")
	var chunks []string
	got, err := parseCursorEvents(context.Background(), strings.NewReader(stream), cursorModelMedium, func(s string) { chunks = append(chunks, s) })
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != `{"findings":[]}` || got.SessionID != "session-1" || got.Model != cursorModelMedium {
		t.Fatalf("result = %+v", got)
	}
	if got.Usage.InputTokens != 10 || got.Usage.OutputTokens != 3 || got.Usage.CacheReadTokens != 2 || got.Usage.CacheCreationTokens != 1 {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if strings.Join(chunks, "") != `{"findings":[]}` {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestParseCursorEventsFailsClosed(t *testing.T) {
	tests := map[string]string{
		"truncated":        `{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}`,
		"duplicate result": strings.Join([]string{cursorInitMedium, cursorSuccess, cursorSuccess}, "\n"),
		"model mismatch":   strings.Join([]string{`{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Low"}`, cursorSuccess}, "\n"),
		"missing model":    strings.Join([]string{`{"type":"system","subtype":"init","session_id":"s"}`, cursorSuccess}, "\n"),
		"malformed":        "{not json}",
		"error result":     strings.Join([]string{cursorInitMedium, `{"type":"result","subtype":"error","is_error":true,"result":"Not logged in","session_id":"s"}`}, "\n"),
	}
	for name, stream := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseCursorEvents(context.Background(), strings.NewReader(stream), cursorModelMedium, nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseCursorEventsAcceptsEmpiricalHighDisplayLabel(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 High"}`,
		cursorSuccess,
	}, "\n")
	if _, err := parseCursorEvents(context.Background(), strings.NewReader(stream), cursorModelHigh, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCursorAuthErrorClassified(t *testing.T) {
	_, err := parseCursorEvents(context.Background(), strings.NewReader(strings.Join([]string{
		cursorInitMedium,
		`{"type":"result","subtype":"error","is_error":true,"result":"Authentication required. Run agent login.","session_id":"s"}`,
	}, "\n")), cursorModelMedium, nil)
	if !IsAuthorizationRequired(err) {
		t.Fatalf("error = %v, want AuthorizationRequired", err)
	}
}

func TestCursorLoggedOutAndExpiredErrorsClassified(t *testing.T) {
	for _, message := range []string{"Not logged in", "Login required", "Session expired"} {
		stream := strings.Join([]string{cursorInitMedium,
			`{"type":"result","subtype":"error","is_error":true,"result":` + strconv.Quote(message) + `,"session_id":"s"}`,
		}, "\n")
		_, err := parseCursorEvents(context.Background(), strings.NewReader(stream), cursorModelMedium, nil)
		if !IsAuthorizationRequired(err) {
			t.Errorf("%q error = %v, want AuthorizationRequired", message, err)
		}
	}
}

func TestCursorAuthErrorRedactsAuthorizationValues(t *testing.T) {
	err := cursorCommandError("exited", errors.New("authentication required"), "visit https://auth.example/secret?code=123")
	if !IsAuthorizationRequired(err) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "auth.example") || strings.Contains(err.Error(), "123") {
		t.Fatalf("authorization value leaked: %v", err)
	}
}

func TestCursorStructuredResultValidatesSchema(t *testing.T) {
	parsed, err := parseCursorEvents(context.Background(), strings.NewReader(strings.Join([]string{cursorInitMedium, cursorSuccess}, "\n")), cursorModelMedium, nil)
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","properties":{"findings":{"type":"array"}},"required":["findings"]}`)
	result, err := finalizeCursorResult(parsed, schema)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Output) != `{"findings":[]}` {
		t.Fatalf("output = %s", result.Output)
	}
}

func TestCursorSchemaTravelsOnlyInStdinPayload(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`)
	prompt, err := cursorStdinPrompt("review", schema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, `"required":["ok"]`) || !strings.HasPrefix(prompt, "review\n") {
		t.Fatalf("stdin payload = %q", prompt)
	}
	args := strings.Join((&cursorAgent{}).buildArgs(cursorModelMedium, false), " ")
	if strings.Contains(args, "required") || strings.Contains(args, "properties") {
		t.Fatalf("schema leaked into argv: %q", args)
	}
}

func TestCursorSupportedVersionExact(t *testing.T) {
	if err := validateCursorVersion("2026.07.17-3e2a980\n"); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorVersion("2026.07.18-deadbeef\n"); err == nil {
		t.Fatal("expected unsupported version error")
	}
}

func TestCursorModelMappingIsBoundedToVerifiedCatalog(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{routing.ModelCursorLow, cursorModelLow},
		{routing.ModelCursorMedium, cursorModelMedium},
		{routing.ModelCursorHigh, cursorModelHigh},
	} {
		got, err := cursorModelForRoute(routing.Decision{EffectiveModel: tc.model})
		if err != nil || got != tc.want {
			t.Fatalf("model %q => %q, %v", tc.model, got, err)
		}
	}
	if _, err := cursorModelForRoute(routing.Decision{EffectiveModel: "auto"}); err == nil {
		t.Fatal("Cursor Auto must fail closed")
	}
	if _, err := cursorModelForRoute(routing.Decision{EffectiveModel: "cursor-grok-4.5-medium-fast"}); err == nil {
		t.Fatal("Fast must not enter production routing")
	}
}

func TestCursorCancellationIsNotAuth(t *testing.T) {
	if IsAuthorizationRequired(errors.New(context.Canceled.Error())) {
		t.Fatal("cancellation must not classify as auth")
	}
}

func TestCursorProcessEnvReplacesAmbientCredentials(t *testing.T) {
	t.Setenv("HOME", "/ambient-home")
	t.Setenv("CURSOR_CONFIG_DIR", "/ambient")
	t.Setenv("AGENT_CLI_CREDENTIAL_STORE", "keychain")
	t.Setenv("CURSOR_API_KEY", "secret")
	t.Setenv("CURSOR_ACCESS_TOKEN", "access-secret")
	t.Setenv("CURSOR_REFRESH_TOKEN", "refresh-secret")
	env := cursorProcessEnv(context.Background(), t.TempDir(), "/isolated-home", "/isolated-profile")
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"HOME=/ambient-home", "CURSOR_CONFIG_DIR=/ambient", "CURSOR_API_KEY=secret", "CURSOR_ACCESS_TOKEN=access-secret", "CURSOR_REFRESH_TOKEN=refresh-secret", "AGENT_CLI_CREDENTIAL_STORE=keychain"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("ambient Cursor credential source survived: %q", forbidden)
		}
	}
	for _, required := range []string{"HOME=/isolated-home", "CURSOR_CONFIG_DIR=/isolated-profile", "AGENT_CLI_CREDENTIAL_STORE=file", "NO_OPEN_BROWSER=1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing isolated env %q", required)
		}
	}
}

func TestCursorConfigDirMustBePrivateAndNotSymlinked(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(dir); err != nil {
		t.Fatal(err)
	}
	unsafeFile := filepath.Join(dir, "state")
	if err := os.WriteFile(unsafeFile, []byte("opaque"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(dir); err == nil {
		t.Fatal("expected non-private profile file refusal")
	}
	if err := os.Chmod(unsafeFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(dir); err == nil {
		t.Fatal("expected public mode refusal")
	}
	link := filepath.Join(t.TempDir(), "profile")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorConfigDir(link); err == nil {
		t.Fatal("expected symlink refusal")
	}
}

func TestCursorHomeDirMustBePrivateAndNotSymlinked(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(home); err != nil {
		t.Fatal(err)
	}
	unsafeFile := filepath.Join(home, "credentials.json")
	if err := os.WriteFile(unsafeFile, []byte("opaque"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(home); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("error = %v, want non-private credential-home file refusal", err)
	}
	if err := os.Chmod(unsafeFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(home); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if err := validateCursorHomeDir(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink refusal", err)
	}
}

func TestVerifyCursorIsolatedWorktree(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "base").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if err := verifyCursorIsolatedWorktree(context.Background(), repo); err == nil {
		t.Fatal("ordinary checkout must not enable Cursor --force")
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "fixture", linked).CombinedOutput(); err != nil {
		t.Fatalf("git worktree add: %v: %s", err, out)
	}
	if err := verifyCursorIsolatedWorktree(context.Background(), linked); err != nil {
		t.Fatal(err)
	}
}

func TestParseCursorEventsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseCursorEvents(ctx, strings.NewReader(cursorInitMedium+"\n"+cursorSuccess), cursorModelMedium, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestCursorMissingProfileIsAuthorizationRequired(t *testing.T) {
	err := validateCursorConfigDir(filepath.Join(t.TempDir(), "missing"))
	if !IsAuthorizationRequired(err) {
		t.Fatalf("error = %v, want authorization-required parking", err)
	}
}

const (
	cursorInitMedium = `{"type":"system","subtype":"init","session_id":"s","model":"Cursor Grok 4.5 Medium"}`
	cursorSuccess    = `{"type":"result","subtype":"success","is_error":false,"result":"{\"findings\":[]}","session_id":"s","usage":{"inputTokens":1,"outputTokens":1}}`
)
