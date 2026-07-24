package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/admission"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAdmissionCallsHaveBoundedContexts(t *testing.T) {
	originalTimeout := admissionCallTimeout
	admissionCallTimeout = 20 * time.Millisecond
	t.Cleanup(func() { admissionCallTimeout = originalTimeout })

	started := time.Now()
	err := runAdmissionCall(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission call error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("admission call took %s, want under one second", elapsed)
	}
}

func TestStartRunUnavailableAdmissionPreventsCancellationAndRunInsert(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo, err := d.InsertRepo(t.TempDir(), "https://example.invalid/repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := d.InsertRun(repo.ID, "feature/current", "old-head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(existing.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}

	mgr := NewRunManager(d, p, func() []pipeline.Step { return nil })
	_, err = mgr.startRun(context.Background(), repo, "feature/current", "new-head", "base", "test", nil, "")
	if !errors.Is(err, admission.ErrCoordinatorUnavailable) {
		t.Fatalf("start error = %v, want ErrCoordinatorUnavailable", err)
	}
	runs, err := d.GetRunsByRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != existing.ID || runs[0].Status != types.RunRunning {
		t.Fatalf("admission failure mutated runs: %#v", runs)
	}
}

func TestRawPushIPCUnavailableAdmissionPreservesExistingRun(t *testing.T) {
	p, d := startTestDaemonWithOptions(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "raw-admission-repo")
	existing, err := d.InsertRun(repo.ID, "feature/current", "old-head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(existing.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	err = client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir(repo.ID), Ref: "refs/heads/feature/current",
		Old: existing.HeadSHA, New: headSHA,
	}, &result)
	if err == nil || !strings.Contains(err.Error(), admission.ErrCoordinatorUnavailable.Error()) {
		t.Fatalf("raw IPC error = %v, want unavailable admission", err)
	}
	runs, err := d.GetRunsByRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != existing.ID || runs[0].Status != types.RunRunning {
		t.Fatalf("raw IPC admission failure mutated runs: %#v", runs)
	}
}

func TestRerunIPCUnavailableAdmissionPreservesExistingRun(t *testing.T) {
	p, d := startTestDaemonWithOptions(t)
	repo, headSHA := setupTestGitRepo(t, p, d, "rerun-admission-repo")
	existing, err := d.InsertRun(repo.ID, "main", headSHA, "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(existing.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.RerunResult
	err = client.Call(ipc.MethodRerun, &ipc.RerunParams{RepoID: repo.ID, Branch: "main"}, &result)
	if err == nil || !strings.Contains(err.Error(), admission.ErrCoordinatorUnavailable.Error()) {
		t.Fatalf("rerun IPC error = %v, want unavailable admission", err)
	}
	runs, err := d.GetRunsByRepo(repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != existing.ID || runs[0].Status != types.RunRunning {
		t.Fatalf("rerun admission failure mutated runs: %#v", runs)
	}
}
