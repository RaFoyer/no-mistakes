package daemon

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestResumeProvisioningRunsRetainsRecoveryQueueOverflow(t *testing.T) {
	_, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-recovery-overflow")
	mgr.provisionQueue = make(chan struct{}, 1)
	mgr.provisionQueue <- struct{}{}
	var runs []*db.Run
	for range 2 {
		run, err := d.InsertRun(repo.ID, "main", headSHA, headSHA)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.SetRunProvisioning(run.ID, "queued", 0, ""); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, run)
	}

	mgr.resumeProvisioningRuns(runs)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mgr.mu.Lock()
		active := len(mgr.provisionCancels)
		mgr.mu.Unlock()
		if active == len(runs) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mgr.mu.Lock()
	active := len(mgr.provisionCancels)
	mgr.mu.Unlock()
	if active != len(runs) {
		t.Fatalf("recovered provisioning cancels = %d, want %d", active, len(runs))
	}
	<-mgr.provisionQueue
	for _, run := range runs {
		if terminal := waitForRunTerminalState(t, d, run.ID); terminal.Status != types.RunCompleted {
			t.Fatalf("recovered overflow run = %+v", terminal)
		}
	}
}

func TestStartRunTerminalizesWhenInitialProvisioningRecordFails(t *testing.T) {
	p, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-record-failure")
	triggerDB, err := sql.Open("sqlite", p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer triggerDB.Close()
	if _, err := triggerDB.Exec(`CREATE TRIGGER fail_provisioning BEFORE INSERT ON lifecycle_events WHEN NEW.event_type = 'provisioning' BEGIN SELECT RAISE(ABORT, 'injected provisioning failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.startRun(context.Background(), repo, "main", headSHA, headSHA, "push", nil, "", nil, nil); err == nil {
		t.Fatal("startRun unexpectedly succeeded")
	}
	runs, err := d.GetRunsByRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != types.RunFailed || runs[0].Error == nil || !strings.Contains(*runs[0].Error, "record provisioning") {
		t.Fatalf("initial provisioning failure projection = %+v", runs)
	}
}

func TestPushReceivedReturnsWhileProvisioningQueued(t *testing.T) {
	p, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-queued")
	fillProvisionSlots(t, mgr)

	type result struct {
		runID string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		runID, err := mgr.HandlePushReceived(context.Background(), &ipc.PushReceivedParams{
			Gate: p.RepoDir(repo.ID),
			Ref:  "refs/heads/main",
			Old:  "0000000000000000000000000000000000000000",
			New:  headSHA,
		})
		done <- result{runID: runID, err: err}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(750 * time.Millisecond):
		t.Fatal("push admission waited for provisioning instead of returning after queueing")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	run, err := d.GetRun(got.runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != types.RunProvisioning || run.ProvisioningPhase != "queued" {
		t.Fatalf("queued run projection = %+v", run)
	}

	releaseOneProvisionSlot(t, mgr)
	if terminal := waitForRunTerminalState(t, d, got.runID); terminal.Status != types.RunCompleted {
		t.Fatalf("terminal run = %+v", terminal)
	}
}

func TestCancelQueuedProvisioningPersistsCancelledRun(t *testing.T) {
	p, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-cancel")
	fillProvisionSlots(t, mgr)

	runID, err := mgr.HandlePushReceived(context.Background(), &ipc.PushReceivedParams{
		Gate: p.RepoDir(repo.ID),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.HandleCancel(runID); err != nil {
		t.Fatal(err)
	}
	run := waitForRunStatus(t, d, runID, types.RunCancelled)
	if run.Error == nil || !strings.Contains(*run.Error, types.RunCancelReasonAbortedByUser) {
		t.Fatalf("cancelled run error = %+v", run.Error)
	}
	if _, err := os.Stat(p.WorktreeDir(repo.ID, runID)); !os.IsNotExist(err) {
		t.Fatalf("queued cancellation created worktree, stat err=%v", err)
	}
}

func TestResumeProvisioningRunsRequeuesAfterRestart(t *testing.T) {
	p, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-restart")
	run, err := d.InsertRun(repo.ID, "main", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetRunProvisioning(run.ID, "worktree", 5, ""); err != nil {
		t.Fatal(err)
	}
	partialWorktree := p.WorktreeDir(repo.ID, run.ID)
	if err := git.WorktreeAdd(context.Background(), p.RepoDir(repo.ID), partialWorktree, headSHA); err != nil {
		t.Fatal(err)
	}

	mgr.resumeProvisioningRuns([]*db.Run{run})
	if terminal := waitForRunTerminalState(t, d, run.ID); terminal.Status != types.RunCompleted {
		t.Fatalf("terminal run = %+v", terminal)
	}
	events, err := d.LifecycleEvents(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "provisioning_completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing provisioning_completed event: %+v", events)
	}
}

func TestProvisioningFailsClosedAndCleansOnlyOwnedWorktreeWhenSourceRemoved(t *testing.T) {
	p, d, mgr, repo, headSHA := newProvisioningTestManager(t, "provisioning-removed-source")
	unrelatedDir := p.WorktreeDir(repo.ID, "parked-unrelated-run")
	if err := os.MkdirAll(unrelatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(unrelatedDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr.provisionHook = func(_ context.Context, phase string, _ *db.Run, _ string) error {
		if phase == "after_worktree" {
			return os.RemoveAll(repo.WorkingPath)
		}
		return nil
	}

	runID, err := mgr.startRun(context.Background(), repo, "main", headSHA, headSHA, "push", nil, "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = waitForRunStatus(t, d, runID, types.RunFailed)
	waitForManagerIdle(t, mgr, 5*time.Second)
	run, err := d.GetRun(runID)
	if err != nil {
		t.Fatal(err)
	}
	if run.ProvisioningError == nil || !strings.Contains(*run.ProvisioningError, "source working directory") {
		t.Fatalf("provisioning error = %v, want actionable removed-source evidence", run.ProvisioningError)
	}
	if _, err := os.Stat(p.WorktreeDir(repo.ID, runID)); !os.IsNotExist(err) {
		t.Fatalf("failed run worktree still exists or stat failed: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve" {
		t.Fatalf("unrelated parked worktree changed: content=%q err=%v", got, err)
	}
}

func newProvisioningTestManager(t *testing.T, repoID string) (*paths.Paths, *db.DB, *RunManager, *db.Repo, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "dtest")
	if err != nil {
		t.Fatal(err)
	}
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
	repo, headSHA := setupTestGitRepo(t, p, d, repoID)
	mgr := NewRunManager(d, p, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})
	t.Cleanup(func() {
		mgr.Shutdown()
		_ = d.Close()
		_ = os.RemoveAll(tmpDir)
	})
	return p, d, mgr, repo, headSHA
}

func fillProvisionSlots(t *testing.T, mgr *RunManager) {
	t.Helper()
	for i := 0; i < cap(mgr.provisionSlots); i++ {
		mgr.provisionSlots <- struct{}{}
	}
	t.Cleanup(func() {
		for {
			select {
			case <-mgr.provisionSlots:
			default:
				return
			}
		}
	})
}

func releaseOneProvisionSlot(t *testing.T, mgr *RunManager) {
	t.Helper()
	select {
	case <-mgr.provisionSlots:
	case <-time.After(1 * time.Second):
		t.Fatal("provision slot was not held")
	}
}

func waitForRunStatus(t *testing.T, d *db.DB, runID string, want types.RunStatus) *db.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := d.GetRun(runID)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil && run.Status == want {
			return run
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("run %s did not reach status %s", runID, want)
	return nil
}
