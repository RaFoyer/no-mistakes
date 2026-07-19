package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/telemetry"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// --- RunManager integration tests ---

func TestPushReceivedTracksRunTelemetry(t *testing.T) {
	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	step := &mockPassStep{name: types.StepReview}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "telemetry-run-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("telemetry-run-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result)
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want %q", run.Status, types.RunCompleted)
	}

	started := recorder.find("run", "action", "started")
	if started == nil {
		t.Fatal("expected run started telemetry event")
	}
	if got := started.fields["trigger"]; got != "push" {
		t.Fatalf("started trigger = %v, want push", got)
	}
	if got := started.fields["agent"]; got != string(types.AgentClaude) {
		t.Fatalf("started agent = %v, want %q", got, types.AgentClaude)
	}
	if got := started.fields["branch_role"]; got != "default" {
		t.Fatalf("started branch_role = %v, want default", got)
	}

	// The executor persists terminal status before its owner goroutine emits
	// terminal telemetry. Wait for that asynchronous handoff instead of
	// assuming it completed in the same scheduling slice, which is especially
	// unreliable on Windows.
	finished := waitForTelemetryEvent(t, recorder, "run", "action", "finished")
	if finished == nil {
		t.Fatal("expected run finished telemetry event")
	}
	if got := finished.fields["status"]; got != string(types.RunCompleted) {
		t.Fatalf("finished status = %v, want %q", got, types.RunCompleted)
	}
	if _, ok := finished.fields["duration_ms"]; !ok {
		t.Fatal("expected duration_ms in run finished telemetry")
	}
}

func TestNormalizeSelectedGitHubConfigDir(t *testing.T) {
	if got, err := normalizeSelectedGitHubConfigDir(nil); err != nil || got != nil {
		t.Fatalf("unselected profile = %#v, %v; want nil, nil", got, err)
	}

	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := normalizeSelectedGitHubConfigDir(&dir)
	if err != nil {
		t.Fatalf("valid profile reference: %v", err)
	}
	if got == nil || *got != canonicalDir {
		t.Fatalf("valid profile reference = %#v, want %q", got, canonicalDir)
	}
}

func TestNormalizeSelectedGitHubConfigDirFailsClosedValueSafely(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "private-profile-name")
	nonDirectory := filepath.Join(t.TempDir(), "profile-file")
	if err := os.WriteFile(nonDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "relative", value: "relative/profile"},
		{name: "leading-space", value: " " + missing},
		{name: "newline", value: "bad\npath"},
		{name: "nul", value: "bad\x00path"},
		{name: "missing", value: missing},
		{name: "non-directory", value: nonDirectory},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			value := tc.value
			got, err := normalizeSelectedGitHubConfigDir(&value)
			if err == nil || got != nil {
				t.Fatalf("normalize(%q) = %#v, %v; want nil error", value, got, err)
			}
			if value != "" && strings.Contains(err.Error(), value) {
				t.Fatalf("error exposed selected path: %v", err)
			}
		})
	}
}

func secureCodexStateRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(resolved, 0o700); err != nil {
		t.Fatal(err)
	}
	return resolved
}

func requireEffectiveUID(t *testing.T) {
	t.Helper()
	if os.Geteuid() < 0 {
		t.Skip("effective uid metadata is unavailable")
	}
}

func TestNormalizeSelectedCodexStateRootUsesMetadataOnly(t *testing.T) {
	requireEffectiveUID(t)
	if got, err := normalizeSelectedCodexStateRoot(nil); err != nil || got != nil {
		t.Fatalf("unselected root = %#v, %v; want nil, nil", got, err)
	}

	root := secureCodexStateRoot(t)
	secret := filepath.Join(root, "must-not-be-opened")
	if err := os.WriteFile(secret, []byte("credential-material-must-not-be-read"), 0o000); err != nil {
		t.Fatal(err)
	}
	got, err := normalizeSelectedCodexStateRoot(&root)
	if err != nil {
		t.Fatalf("valid state root: %v", err)
	}
	if got == nil || *got != filepath.Clean(root) {
		t.Fatalf("valid state root = %#v, want %q", got, filepath.Clean(root))
	}
}

func TestNormalizeSelectedCodexStateRootFailsClosedValueSafely(t *testing.T) {
	base := secureCodexStateRoot(t)
	missing := filepath.Join(base, "private-state-name")
	nonDirectory := filepath.Join(base, "state-file")
	if err := os.WriteFile(nonDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	wrongMode := filepath.Join(base, "wrong-mode")
	if err := os.Mkdir(wrongMode, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "relative", value: "relative/state"},
		{name: "leading-space", value: " " + missing},
		{name: "newline", value: "bad\npath"},
		{name: "unicode-control", value: "bad\u0085path"},
		{name: "nul", value: "bad\x00path"},
		{name: "missing", value: missing},
		{name: "non-directory", value: nonDirectory},
		{name: "wrong-mode", value: wrongMode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.value
			got, err := normalizeSelectedCodexStateRoot(&value)
			if err == nil || got != nil {
				t.Fatalf("normalize(%q) = %#v, %v; want nil error", value, got, err)
			}
			if err.Error() != codexStateRootUnavailable {
				t.Fatalf("error = %q, want constant value-safe error", err)
			}
			if value != "" && strings.Contains(err.Error(), value) {
				t.Fatalf("error exposed selected path: %v", err)
			}
		})
	}
}

func TestNormalizeSelectedCodexStateRootRejectsSpecialModeBits(t *testing.T) {
	root := secureCodexStateRoot(t)
	if err := os.Chmod(root, 0o1700); err != nil {
		t.Skipf("set sticky mode: %v", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSticky == 0 {
		t.Skip("filesystem did not retain sticky mode")
	}
	if got, err := normalizeSelectedCodexStateRoot(&root); err == nil || got != nil || err.Error() != codexStateRootUnavailable {
		t.Fatalf("special-mode root normalized to %#v with error %v; want constant rejection", got, err)
	}
}

func TestNormalizeSelectedCodexStateRootRejectsFinalAndAncestorSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires privileges on Windows")
	}
	base := secureCodexStateRoot(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	finalLink := filepath.Join(base, "final-link")
	if err := os.Symlink(target, finalLink); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(target, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorLink := filepath.Join(base, "ancestor-link")
	if err := os.Symlink(target, ancestorLink); err != nil {
		t.Fatal(err)
	}
	viaAncestor := filepath.Join(ancestorLink, "child")
	viaCollapsedAncestor := ancestorLink + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "target"

	for _, value := range []string{finalLink, viaAncestor, viaCollapsedAncestor} {
		value := value
		if got, err := normalizeSelectedCodexStateRoot(&value); err == nil || got != nil || err.Error() != codexStateRootUnavailable {
			t.Fatalf("symlink path normalized to %#v with error %v; want constant rejection", got, err)
		}
	}
}

type ownerOverrideFileInfo struct {
	os.FileInfo
	metadata any
}

func (i ownerOverrideFileInfo) Sys() any { return i.metadata }

func TestCodexStateRootOwnershipMustMatchEffectiveUser(t *testing.T) {
	if os.Geteuid() < 0 {
		t.Skip("effective uid metadata is unavailable")
	}
	root := secureCodexStateRoot(t)
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !ownedByEffectiveUser(info) {
		t.Fatal("current-user directory was not recognized as owned")
	}
	wrongOwner := ownerOverrideFileInfo{
		FileInfo: info,
		metadata: &struct{ Uid uint32 }{Uid: uint32(os.Geteuid() + 1)},
	}
	if ownedByEffectiveUser(wrongOwner) {
		t.Fatal("different-owner metadata was accepted")
	}
}

func TestCodexStateRootRecoveryRequirementFailsClosedWithoutAmbientSubstitution(t *testing.T) {
	t.Setenv("CODEX_HOME", secureCodexStateRoot(t))
	required := &db.Run{RequiresCodexStateRoot: true}
	if got, err := codexStateRootForRun(required, nil); err == nil || got != "" || err.Error() != codexStateRootUnavailable {
		t.Fatalf("required recovery root = %q, %v; want constant fail-closed error", got, err)
	}
	unselected := &db.Run{}
	if got, err := codexStateRootForRun(unselected, nil); err != nil || got != "" {
		t.Fatalf("unselected recovery root = %q, %v; want absent selection allowed", got, err)
	}
}

func TestPrepareRecoveredRunFailsClosedBeforeAgentCreationWhenCodexRootCustodyIsLost(t *testing.T) {
	t.Setenv("CODEX_HOME", secureCodexStateRoot(t))
	parkedAt := time.Now().Unix()
	run := &db.Run{
		Status:                 types.RunRunning,
		AwaitingAgentSince:     &parkedAt,
		Branch:                 "feature/recovery",
		RequiresCodexStateRoot: true,
	}
	m := NewRunManager(nil, nil, nil)
	plan, err := m.prepareRecoveredRun(context.Background(), run)
	if err == nil || plan != nil || err.Error() != codexStateRootUnavailable {
		t.Fatalf("recovered plan = %#v, %v; want value-safe failure before ambient agent creation", plan, err)
	}
}

func TestPushReceivedRejectsInvalidCodexStateRootWithoutCreatingRun(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	_, headSHA := setupTestGitRepo(t, p, d, "invalid-codex-state-run-repo")
	missing := filepath.Join(secureCodexStateRoot(t), "missing")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("invalid-codex-state-run-repo"), Ref: "refs/heads/main",
		Old: strings.Repeat("0", 40), New: headSHA, CodexStateRoot: &missing,
	}, &result)
	if err == nil || err.Error() != codexStateRootUnavailable {
		t.Fatalf("invalid state-root error = %v, want value-safe failure", err)
	}
	runs, err := d.GetRunsByRepo("invalid-codex-state-run-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("invalid state root created %d runs, want 0", len(runs))
	}
}

func TestPushReceivedKeepsCodexStateRootInMemoryAndOutOfSurfaces(t *testing.T) {
	requireEffectiveUID(t)
	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})
	_, headSHA := setupTestGitRepo(t, p, d, "codex-state-run-repo")
	root := secureCodexStateRoot(t)
	contents := "credential-material-must-not-be-read"
	if err := os.WriteFile(filepath.Join(root, "state.json"), []byte(contents), 0o000); err != nil {
		t.Fatal(err)
	}

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("codex-state-run-repo"), Ref: "refs/heads/main",
		Old: strings.Repeat("0", 40), New: headSHA, CodexStateRoot: &root,
	}, &result); err != nil {
		t.Fatal(err)
	}
	run := waitForRunTerminalState(t, d, result.RunID)
	if !run.RequiresCodexStateRoot {
		t.Fatal("selected state-root run did not persist its private requirement boolean")
	}
	steps, err := d.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := json.Marshal(runToInfo(d, run, steps))
	if err != nil {
		t.Fatal(err)
	}
	assertValueSafe := func(surface string, data []byte) {
		t.Helper()
		for _, forbidden := range []string{root, contents} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s exposed private Codex state-root custody", surface)
			}
		}
	}
	assertValueSafe("status/read projection", projection)
	if strings.Contains(string(projection), "requires_codex_state_root") {
		t.Fatal("status/read projection exposed private recovery requirement")
	}

	if waitForTelemetryEvent(t, recorder, "run", "action", "finished") == nil {
		t.Fatal("run did not emit terminal telemetry")
	}
	recorder.mu.Lock()
	events := append([]recordedTelemetryEvent(nil), recorder.events...)
	recorder.mu.Unlock()
	for _, event := range events {
		fields, err := json.Marshal(event.fields)
		if err != nil {
			t.Fatal(err)
		}
		assertValueSafe("telemetry", fields)
	}

	if err := filepath.Walk(p.Root(), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertValueSafe("database/log/internal state", data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRerunTransportsCodexStateRootAndValidatesBeforeCreation(t *testing.T) {
	requireEffectiveUID(t)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})
	_, headSHA := setupTestGitRepo(t, p, d, "codex-state-rerun-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var first ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("codex-state-rerun-repo"), Ref: "refs/heads/main",
		Old: strings.Repeat("0", 40), New: headSHA,
	}, &first); err != nil {
		t.Fatal(err)
	}
	waitForRunTerminalState(t, d, first.RunID)

	missing := filepath.Join(secureCodexStateRoot(t), "missing")
	var invalid ipc.RerunResult
	err = client.Call(ipc.MethodRerun, &ipc.RerunParams{
		RepoID: "codex-state-rerun-repo", Branch: "main", CodexStateRoot: &missing,
	}, &invalid)
	if err == nil || err.Error() != codexStateRootUnavailable {
		t.Fatalf("invalid rerun error = %v, want value-safe failure", err)
	}
	runs, err := d.GetRunsByRepo("codex-state-rerun-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("invalid rerun created a row: got %d runs, want 1", len(runs))
	}

	root := secureCodexStateRoot(t)
	var valid ipc.RerunResult
	if err := client.Call(ipc.MethodRerun, &ipc.RerunParams{
		RepoID: "codex-state-rerun-repo", Branch: "main", CodexStateRoot: &root,
	}, &valid); err != nil {
		t.Fatal(err)
	}
	run := waitForRunTerminalState(t, d, valid.RunID)
	if !run.RequiresCodexStateRoot {
		t.Fatal("rerun did not retain the private state-root recovery requirement")
	}
}

func TestPushReceivedScopesSelectedGitHubConfigDirToPublicationSteps(t *testing.T) {
	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	reviewEnv := make(chan []string, 1)
	prEnv := make(chan []string, 1)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{
			&mockEnvCaptureStep{name: types.StepReview, env: reviewEnv},
			&mockEnvCaptureStep{name: types.StepPR, env: prEnv},
		}
	})
	_, headSHA := setupTestGitRepo(t, p, d, "profile-run-repo")
	profileDir := t.TempDir()
	profileContents := "credential-material-must-not-be-read"
	profileFile := filepath.Join(profileDir, "hosts.yml")
	if err := os.WriteFile(profileFile, []byte(profileContents), 0o000); err != nil {
		t.Fatal(err)
	}
	canonicalDir, err := filepath.EvalSymlinks(profileDir)
	if err != nil {
		t.Fatal(err)
	}

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("profile-run-repo"), Ref: "refs/heads/main",
		Old: strings.Repeat("0", 40), New: headSHA, GitHubConfigDir: &profileDir,
	}, &result); err != nil {
		t.Fatal(err)
	}
	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want completed", run.Status)
	}
	if !run.RequiresGitHubPublicationProfile {
		t.Fatal("selected-profile run did not persist its private requirement boolean")
	}
	if env := <-reviewEnv; len(env) != 0 {
		t.Fatalf("review received publication profile env: %#v", env)
	}
	if env := <-prEnv; len(env) != 1 || env[0] != "GH_CONFIG_DIR="+canonicalDir {
		t.Fatalf("PR env = %#v, want selected profile only", env)
	}
	if waitForTelemetryEvent(t, recorder, "run", "action", "finished") == nil {
		t.Fatal("run did not emit terminal telemetry")
	}

	stepRows, err := d.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	readProjection, err := json.Marshal(runToInfo(d, run, stepRows))
	if err != nil {
		t.Fatal(err)
	}
	assertValueSafe := func(surface string, data []byte) {
		t.Helper()
		for _, forbidden := range []string{canonicalDir, profileContents} {
			if strings.Contains(string(data), forbidden) {
				t.Fatalf("%s exposed selected publication profile material", surface)
			}
		}
	}
	assertValueSafe("status/read projection", readProjection)
	if strings.Contains(string(readProjection), "requires_github_publication_profile") {
		t.Fatal("status/read projection exposed private custody state")
	}

	recorder.mu.Lock()
	events := append([]recordedTelemetryEvent(nil), recorder.events...)
	recorder.mu.Unlock()
	for _, event := range events {
		fields, err := json.Marshal(event.fields)
		if err != nil {
			t.Fatal(err)
		}
		assertValueSafe("telemetry", fields)
	}

	if err := filepath.Walk(p.Root(), func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		assertValueSafe("database/log/internal state", data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPushReceivedRejectsInvalidGitHubConfigDirWithoutCreatingRun(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepPR}} })
	_, headSHA := setupTestGitRepo(t, p, d, "invalid-profile-run-repo")
	missing := filepath.Join(t.TempDir(), "private-profile-name")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("invalid-profile-run-repo"), Ref: "refs/heads/main",
		Old: strings.Repeat("0", 40), New: headSHA, GitHubConfigDir: &missing,
	}, &result)
	if err == nil || err.Error() != githubPublicationProfileUnavailable {
		t.Fatalf("invalid profile error = %v, want value-safe failure", err)
	}
	runs, err := d.GetRunsByRepo("invalid-profile-run-repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("invalid profile created %d runs, want 0", len(runs))
	}
}

func TestPushReceivedSkipStepsConfiguresExecutor(t *testing.T) {
	review := &mockPassStep{name: types.StepReview}
	testStep := &mockPassStep{name: types.StepTest}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{review, testStep}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "skip-run-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate:      p.RepoDir("skip-run-repo"),
		Ref:       "refs/heads/main",
		Old:       "0000000000000000000000000000000000000000",
		New:       headSHA,
		SkipSteps: []types.StepName{types.StepReview},
	}, &result)
	if err != nil {
		t.Fatal(err)
	}

	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, want %q", run.Status, types.RunCompleted)
	}
	if got := review.execCnt.Load(); got != 0 {
		t.Fatalf("review executed %d times, want 0", got)
	}
	if got := testStep.execCnt.Load(); got != 1 {
		t.Fatalf("test executed %d times, want 1", got)
	}
	steps, err := d.GetStepsByRun(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.StepName == types.StepReview && step.Status != types.StepStatusSkipped {
			t.Fatalf("review status = %s, want %s", step.Status, types.StepStatusSkipped)
		}
	}
}

func TestPushReceivedAllowsDifferentBranchRunsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "concurrent-branch-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var first ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("concurrent-branch-repo"),
		Ref:  "refs/heads/feature/one",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &first); err != nil {
		t.Fatal(err)
	}
	waitForStartedBranch(t, started, "feature/one")

	var second ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("concurrent-branch-repo"),
		Ref:  "refs/heads/feature/two",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &second); err != nil {
		t.Fatal(err)
	}
	waitForStartedBranch(t, started, "feature/two")

	for _, tc := range []struct {
		branch string
		runID  string
	}{
		{branch: "feature/one", runID: first.RunID},
		{branch: "feature/two", runID: second.RunID},
	} {
		active, err := d.GetActiveRun("concurrent-branch-repo", tc.branch)
		if err != nil {
			t.Fatalf("get active run for %s: %v", tc.branch, err)
		}
		if active == nil {
			t.Fatalf("expected active run for %s", tc.branch)
		}
		if active.ID != tc.runID {
			t.Fatalf("active run for %s = %s, want %s", tc.branch, active.ID, tc.runID)
		}
		if active.Status != types.RunRunning {
			t.Fatalf("active run for %s status = %s, want running", tc.branch, active.Status)
		}
	}
}

type notifyBlockStep struct {
	name    types.StepName
	started chan<- string
}

func (s *notifyBlockStep) Name() types.StepName { return s.name }

func (s *notifyBlockStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	select {
	case s.started <- sctx.Run.Branch:
	default:
	}
	<-sctx.Ctx.Done()
	return nil, sctx.Ctx.Err()
}

func waitForStartedBranch(t *testing.T, started <-chan string, branch string) {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case got := <-started:
			if got == branch {
				return
			}
		case <-timeout:
			t.Fatalf("run for branch %s did not start", branch)
		}
	}
}

// TestPushReceivedConcurrentDifferentBranchRunsAvoidSharedConfigLock fires two
// branch pushes for the same repo at the same time so both runs hit worktree
// creation and git-identity setup concurrently. All runs share one gate bare
// repo, so writing identity with `git config --local` (which targets the bare's
// shared config) made the two startups race on <bare>/config.lock and fail one
// run with "could not lock config file ...: File exists". CopyLocalUserIdentity
// now writes per-worktree, so the startups no longer contend. The race window
// is during synchronous startRun, so a failure surfaces directly as the
// push_received call's error. macOS-only in practice (Linux file locking and
// timing hide it), but the assertion is platform-independent.
func TestPushReceivedConcurrentDifferentBranchRunsAvoidSharedConfigLock(t *testing.T) {
	started := make(chan string, 2)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&notifyBlockStep{name: types.StepReview, started: started}}
	})

	const repoID = "concurrent-config-lock-repo"
	_, headSHA := setupTestGitRepo(t, p, d, repoID)

	// Mirror a real gate: enable the per-worktree config isolation that
	// `no-mistakes init` installs, which is what lets identity writes avoid the
	// shared config.lock.
	if err := git.IsolateHooksPath(context.Background(), p.RepoDir(repoID)); err != nil {
		t.Fatalf("isolate hooks path: %v", err)
	}

	branches := []string{"feature/one", "feature/two"}
	errs := make([]error, len(branches))
	var wg sync.WaitGroup
	for i, br := range branches {
		wg.Add(1)
		go func(i int, br string) {
			defer wg.Done()
			// A dedicated client per goroutine: a single client serializes
			// calls, which would defeat the concurrency we are testing.
			client, err := ipc.Dial(p.Socket())
			if err != nil {
				errs[i] = err
				return
			}
			defer client.Close()
			var res ipc.PushReceivedResult
			errs[i] = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
				Gate: p.RepoDir(repoID),
				Ref:  "refs/heads/" + br,
				Old:  "0000000000000000000000000000000000000000",
				New:  headSHA,
			}, &res)
		}(i, br)
	}
	wg.Wait()

	for i, br := range branches {
		if errs[i] != nil {
			t.Fatalf("concurrent push for %s failed: %v", br, errs[i])
		}
	}

	// Drain both start signals regardless of which run won the race to begin,
	// then confirm both branches have a live, error-free run.
	gotStarted := make(map[string]bool, len(branches))
	for range branches {
		select {
		case b := <-started:
			gotStarted[b] = true
		case <-time.After(3 * time.Second):
			t.Fatalf("a concurrent run did not start (started so far: %v)", gotStarted)
		}
	}

	for _, br := range branches {
		if !gotStarted[br] {
			t.Fatalf("run for branch %s did not start", br)
		}
		active, err := d.GetActiveRun(repoID, br)
		if err != nil {
			t.Fatalf("get active run for %s: %v", br, err)
		}
		if active == nil {
			t.Fatalf("expected active run for %s", br)
		}
		if active.Status != types.RunRunning {
			t.Fatalf("active run for %s status = %s, want running (error: %v)", br, active.Status, active.Error)
		}
	}
}

func TestRerunSkipStepsConfiguresExecutor(t *testing.T) {
	review := &mockPassStep{name: types.StepReview}
	testStep := &mockPassStep{name: types.StepTest}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{review, testStep}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "skip-rerun-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var first ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("skip-rerun-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &first)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunTerminalState(t, d, first.RunID)

	var second ipc.RerunResult
	err = client.Call(ipc.MethodRerun, &ipc.RerunParams{
		RepoID:    "skip-rerun-repo",
		Branch:    "main",
		SkipSteps: []types.StepName{types.StepReview},
	}, &second)
	if err != nil {
		t.Fatal(err)
	}
	waitForRunTerminalState(t, d, second.RunID)

	if got := review.execCnt.Load(); got != 1 {
		t.Fatalf("review executed %d times, want 1", got)
	}
	if got := testStep.execCnt.Load(); got != 2 {
		t.Fatalf("test executed %d times, want 2", got)
	}
	steps, err := d.GetStepsByRun(second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.StepName == types.StepReview && step.Status != types.StepStatusSkipped {
			t.Fatalf("review status = %s, want %s", step.Status, types.StepStatusSkipped)
		}
	}
}

func TestPushReceivedReturnsBeforeIntentSummarization(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)

	step := &mockPassStep{name: types.StepReview}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	slowClaude := writeSlowMockClaude(t, t.TempDir())
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: claude\nagent_path_override:\n  claude: "+slowClaude+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo, headSHA := setupTestGitRepo(t, p, d, "intent-start-run-repo")
	writeManagerClaudeFixture(t, fakeHome, repo.WorkingPath, []string{
		`{"type":"user","cwd":` + testJSONString(t, repo.WorkingPath) + `,"timestamp":"2026-04-18T02:15:37.407Z","uuid":"u1","sessionId":"s1","message":{"role":"user","content":"please update test.txt"}}`,
	})

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	started := time.Now()
	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("intent-start-run-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result)
	if err != nil {
		t.Fatal(err)
	}
	// The 3s slowClaude script is not on this test's synchronous path (the
	// review step here is a mockPassStep and the "claude" agent is explicit,
	// so ResolveAgent never probes it): what this bound really guards is
	// startRun's synchronous git plumbing (worktree add, identity copy,
	// fetch, resolve-ref, config loads) staying well clear of the 3s the
	// pipeline goroutine's slow agent call would take if it ever ran inline.
	// Windows CI process-spawn overhead across those several git subprocess
	// calls is much higher than on macOS/Linux, so Windows gets generous
	// headroom while non-Windows keeps the tight bound that would catch a
	// real regression in startRun's synchronous git plumbing.
	maxElapsed := 2500 * time.Millisecond
	if runtimeGOOS == "windows" {
		maxElapsed = 8 * time.Second
	}
	if elapsed := time.Since(started); elapsed > maxElapsed {
		t.Fatalf("PushReceived took %s, want under %s", elapsed, maxElapsed)
	}
	if result.RunID == "" {
		t.Fatal("expected non-empty run ID")
	}

	waitForRunTerminalState(t, d, result.RunID)
}

func TestStartRunReturnsWhileProvisionerBlockedThirtySeconds(t *testing.T) {
	p, d := newProvisioningManagerDB(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "blocked-provision-repo")
	blocked := make(chan string, 1)
	exited := make(chan struct{})
	m := NewRunManager(d, p, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	m.provisionHook = func(ctx context.Context, phase string, run *db.Run, _ string) error {
		if phase != "after_worktree" {
			return nil
		}
		blocked <- run.ID
		defer close(exited)
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(m.Shutdown)

	started := time.Now()
	runID, err := m.startRun(context.Background(), repo, "main", headSHA, headSHA, "push", nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("startRun took %s with blocked provisioner, want prompt admission", elapsed)
	}
	select {
	case got := <-blocked:
		if got != runID {
			t.Fatalf("blocked run = %s, want %s", got, runID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("provisioner did not reach blocking hook")
	}
	select {
	case <-exited:
		t.Fatal("provisioner exited before proving a 30 second blockage")
	case <-time.After(30 * time.Second):
	}
	if err := m.HandleCancel(runID); err != nil {
		t.Fatal(err)
	}
	waitForChannel(t, exited, 5*time.Second, "blocked provisioner exit")
	waitForManagerIdle(t, m, 5*time.Second)
	if _, err := os.Stat(p.WorktreeDir(repo.ID, runID)); !os.IsNotExist(err) {
		t.Fatalf("cancelled provisioning worktree still exists or stat failed: %v", err)
	}
}

func TestProvisioningSupersessionWaitsForCancelledProvisioner(t *testing.T) {
	p, d := newProvisioningManagerDB(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "supersede-provision-repo")
	firstBlocked := make(chan struct{})
	firstCancelled := make(chan struct{})
	allowFirstExit := make(chan struct{})
	var firstRun atomic.Bool
	m := NewRunManager(d, p, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	m.provisionHook = func(ctx context.Context, phase string, _ *db.Run, _ string) error {
		if phase != "after_worktree" {
			return nil
		}
		if firstRun.CompareAndSwap(false, true) {
			close(firstBlocked)
			<-ctx.Done()
			close(firstCancelled)
			<-allowFirstExit
			return ctx.Err()
		}
		return nil
	}
	t.Cleanup(m.Shutdown)

	firstID, err := m.startRun(context.Background(), repo, "main", headSHA, headSHA, "push", nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitForChannel(t, firstBlocked, 5*time.Second, "first provisioning block")
	secondDone := make(chan error, 1)
	go func() {
		_, err := m.startRun(context.Background(), repo, "main", headSHA, headSHA, "push", nil, "", nil, nil)
		secondDone <- err
	}()
	waitForChannel(t, firstCancelled, 5*time.Second, "first provisioning cancel")
	select {
	case err := <-secondDone:
		t.Fatalf("second run returned before cancelled provisioner exited: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(allowFirstExit)
	if err := <-secondDone; err != nil {
		t.Fatalf("second run after provisioner exit: %v", err)
	}
	waitForManagerIdle(t, m, 5*time.Second)
	if _, err := os.Stat(p.WorktreeDir(repo.ID, firstID)); !os.IsNotExist(err) {
		t.Fatalf("superseded provisioning worktree still exists or stat failed: %v", err)
	}
}

func TestProvisioningWorkerSlotsBoundActiveSetup(t *testing.T) {
	p, d := newProvisioningManagerDB(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "bounded-provision-repo")
	started := make(chan string, 3)
	release := make(chan struct{})
	m := NewRunManager(d, p, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	m.provisionHook = func(ctx context.Context, phase string, run *db.Run, _ string) error {
		if phase != "after_worktree" {
			return nil
		}
		started <- run.Branch
		select {
		case <-release:
			return os.ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	t.Cleanup(m.Shutdown)

	for _, branch := range []string{"feature/one", "feature/two", "feature/three"} {
		if _, err := m.startRun(context.Background(), repo, branch, headSHA, headSHA, "push", nil, "", nil, nil); err != nil {
			t.Fatalf("start %s: %v", branch, err)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case branch := <-started:
			seen[branch] = true
		case <-time.After(5 * time.Second):
			t.Fatal("expected two active provisioning workers")
		}
	}
	select {
	case branch := <-started:
		t.Fatalf("third provisioning worker started before slot release: %s", branch)
	case <-time.After(200 * time.Millisecond):
	}
	release <- struct{}{}
	select {
	case branch := <-started:
		if seen[branch] {
			t.Fatalf("duplicate started branch %s", branch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("third provisioning worker did not start after slot release")
	}
	close(release)
	waitForManagerIdle(t, m, 5*time.Second)
}

func TestResumeProvisioningRunsRegistersWaitHandleBeforeRequeue(t *testing.T) {
	p, d := newProvisioningManagerDB(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "restart-provision-repo")
	run, err := d.InsertRun(repo.ID, "main", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunProvisioning(run.ID, "worktree", 5, ""); err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	m := NewRunManager(d, p, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	m.provisionHook = func(ctx context.Context, phase string, _ *db.Run, _ string) error {
		if phase != "after_worktree" {
			return nil
		}
		close(blocked)
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(m.Shutdown)

	m.resumeProvisioningRuns([]*db.Run{run})
	m.mu.Lock()
	done := m.dones[run.ID]
	m.mu.Unlock()
	if done == nil {
		t.Fatal("restart requeue did not register a done channel before provisioning resumed")
	}
	waitForChannel(t, blocked, 5*time.Second, "requeued provisioning block")
	if err := m.HandleCancel(run.ID); err != nil {
		t.Fatal(err)
	}
	waitForChannel(t, done, 5*time.Second, "requeued provisioning done")
	waitForManagerIdle(t, m, 5*time.Second)
}

func newProvisioningManagerDB(t *testing.T) (*paths.Paths, *db.DB) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "dtest")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	p := paths.WithRoot(tmpDir)
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mockClaude := writeMockClaude(t, t.TempDir())
	if err := os.WriteFile(p.ConfigFile(), []byte("agent: claude\nagent_path_override:\n  claude: "+mockClaude+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return p, d
}

func waitForChannel(t *testing.T, ch <-chan struct{}, timeout time.Duration, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("timed out waiting for %s\n%s", label, buf[:n])
	}
}

func waitForManagerIdle(t *testing.T, m *RunManager, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	waitForChannel(t, done, timeout, "run manager goroutines")
}

func writeManagerClaudeFixture(t *testing.T, home, repoCWD string, lines []string) {
	t.Helper()
	encoded := testClaudeProjectDirName(repoCWD)
	dir := filepath.Join(home, ".claude", "projects", encoded)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-uuid-1.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPushReceivedTracksRunTelemetryAfterPanic(t *testing.T) {
	recorder := &telemetryRecorder{}
	restore := telemetry.SetDefaultForTesting(recorder)
	defer restore()

	step := &mockPanicStep{name: types.StepReview}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	_, headSHA := setupTestGitRepo(t, p, d, "telemetry-panic-repo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("telemetry-panic-repo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := d.GetRun(result.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil && run.Error != nil && strings.Contains(*run.Error, "internal panic") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	finished := recorder.find("run", "action", "finished")
	if finished == nil {
		t.Fatal("expected run finished telemetry event after panic")
	}
	if got := finished.fields["status"]; got != string(types.RunFailed) {
		t.Fatalf("finished status = %v, want %q", got, types.RunFailed)
	}
	if _, ok := finished.fields["duration_ms"]; !ok {
		t.Fatal("expected duration_ms in run finished telemetry after panic")
	}
	for _, field := range []string{"agent_invocations", "resumed_invocations", "fallback_invocations"} {
		if got, ok := finished.fields[field]; !ok || got != 0 {
			t.Fatalf("%s = %v, want 0", field, got)
		}
	}
}

func TestPushReceivedDemoModeBypassesAgentResolution(t *testing.T) {
	t.Setenv("NM_DEMO", "1")

	step := &mockPassStep{name: types.StepReview}
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{step}
	})

	if err := os.WriteFile(p.ConfigFile(), []byte("agent: claude\nagent_path_override:\n  claude: /path/that/does/not/exist\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, headSHA := setupTestGitRepo(t, p, d, "testrepo-demo")

	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("testrepo-demo"),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == "" {
		t.Fatal("expected non-empty run ID")
	}

	waitForRunTerminalState(t, d, result.RunID)
	run, err := d.GetRun(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != types.RunCompleted {
		var runErr string
		if run.Error != nil {
			runErr = *run.Error
		}
		t.Fatalf("run status = %q, want %q (error: %s)", run.Status, types.RunCompleted, runErr)
	}
	if step.execCnt.Load() == 0 {
		t.Error("mock step was never executed")
	}
}
