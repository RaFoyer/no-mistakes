package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestBeginHandoffQuiesceRefusesPublicationProfileCustody(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, err := database.InsertRepo("/tmp/project", "git@example.test:project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertRunWithOptions(repo.ID, "feature", "head", "base", db.InsertRunOptions{RequiresGitHubPublicationProfile: true}); err != nil {
		t.Fatal(err)
	}
	handoff := beginTestHandoff(t, database)
	mgr := NewRunManager(database, p, nil)

	err = mgr.BeginHandoffQuiesce(handoff.ID, "generation-a")
	if err == nil || !strings.Contains(err.Error(), "publication profile") {
		t.Fatalf("BeginHandoffQuiesce error = %v, want profile custody refusal", err)
	}
	got, err := database.GetHandoff(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != db.HandoffPhasePreparing || mgr.HandoffQuiescing() {
		t.Fatalf("refused quiesce mutated state: handoff=%+v quiescing=%t", got, mgr.HandoffQuiescing())
	}
}

func TestBeginHandoffQuiesceRefusesDurableProvisioningState(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, err := database.InsertRepo("/tmp/project", "git@example.test:project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertRun(repo.ID, "feature", "head", "base"); err != nil {
		t.Fatal(err)
	}
	handoff := beginTestHandoff(t, database)
	mgr := NewRunManager(database, p, nil)

	err = mgr.BeginHandoffQuiesce(handoff.ID, "generation-a")
	if err == nil || !strings.Contains(err.Error(), "provisioning") {
		t.Fatalf("BeginHandoffQuiesce error = %v, want durable provisioning refusal", err)
	}
	got, err := database.GetHandoff(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != db.HandoffPhasePreparing || mgr.HandoffQuiescing() {
		t.Fatalf("refused quiesce mutated state: handoff=%+v quiescing=%t", got, mgr.HandoffQuiescing())
	}
}

func TestPrepareRecoveredHandoffRequiresAdoptedCompatibleTarget(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, err := database.InsertRepo("/tmp/project", "git@example.test:project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	run.Status = types.RunRunning
	handoff := beginTestHandoff(t, database)
	if err := database.TransitionHandoff(handoff.ID, db.HandoffPhaseQuiescing, "test quiesce"); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertRunHandoffCheckpoint(db.RunHandoffCheckpoint{
		RunID: run.ID, HandoffID: handoff.ID, Generation: "generation-a",
		NextStep: string(types.StepTest), Worktree: "/tmp/worktree", HeadSHA: "head",
		State: db.CheckpointStateParked,
	}); err != nil {
		t.Fatal(err)
	}

	mgr := NewRunManager(database, p, nil)
	plan, err := mgr.prepareRecoveredRun(context.Background(), run)
	if err == nil || plan != nil || !strings.Contains(err.Error(), "not adopted") {
		t.Fatalf("prepareRecoveredRun = %#v, %v; want pre-adoption refusal", plan, err)
	}
}

func TestManagerCheckpointPersistsVerifiedBoundaryAndCompletesQuiesce(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, headSHA := setupTestGitRepo(t, p, database, "handoff-repo")
	run, err := database.InsertRun(repo.ID, "feature", headSHA, headSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunRunning); err != nil {
		t.Fatal(err)
	}
	first, err := database.InsertStepResult(run.ID, types.StepReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.InsertStepResult(run.ID, types.StepTest); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteStepWithStatus(first.ID, types.StepStatusCompleted, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
	handoff := beginTestHandoff(t, database)
	mgr := NewRunManager(database, p, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}, &mockPassStep{name: types.StepTest}}
	})
	if err := mgr.BeginHandoffQuiesce(handoff.ID, "generation-a"); err != nil {
		t.Fatal(err)
	}
	parked, err := mgr.checkpointForHandoff(pipeline.HandoffCheckpointRequest{
		Run: run, WorkDir: filepath.Clean(repo.WorkingPath), NextStep: types.StepTest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !parked {
		t.Fatal("checkpoint request should park while quiescing")
	}
	checkpoint, err := database.GetRunHandoffCheckpoint(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint == nil || checkpoint.HeadSHA != headSHA || checkpoint.State != db.CheckpointStateParked {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	got, err := database.GetHandoff(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != db.HandoffPhaseCheckpointed {
		t.Fatalf("handoff phase = %q, want checkpointed", got.Phase)
	}
}

func TestStartRunDurablyQueuesWithoutExecutionWhileQuiescing(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	repo, err := database.InsertRepo("/tmp/project", "git@example.test:project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	handoff := beginTestHandoff(t, database)
	mgr := NewRunManager(database, p, nil)
	if err := mgr.BeginHandoffQuiesce(handoff.ID, "generation-a"); err != nil {
		t.Fatal(err)
	}
	queuedID, err := mgr.startRun(context.Background(), repo, "feature", "head", "base", "push", []types.StepName{types.StepReview}, "intent", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	items, err := database.QueuedDaemonAdmissions(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != queuedID || len(items[0].SkipSteps) != 1 || items[0].SkipSteps[0] != string(types.StepReview) {
		t.Fatalf("queued admissions = %+v", items)
	}
	if active, err := database.GetActiveRuns(); err != nil {
		t.Fatal(err)
	} else if len(active) != 0 {
		t.Fatalf("quiesced admission started a run: %+v", active)
	}
}

func beginTestHandoff(t *testing.T, database *db.DB) *db.Handoff {
	t.Helper()
	if err := database.RegisterDaemonGeneration(db.DaemonGeneration{Generation: "generation-a", Build: "build-a", Protocol: 1, Schema: 1}); err != nil {
		t.Fatal(err)
	}
	handoff, err := database.BeginHandoff(db.HandoffSpec{
		SourceGeneration: "generation-a", TargetBuild: "build-b",
		TargetProtocolMin: 1, TargetProtocolMax: 1, TargetSchemaMin: 1, TargetSchemaMax: 1,
		TargetPath: "/immutable/build-b", TargetSHA256: "target-sha",
		RollbackPath: "/immutable/build-a", RollbackSHA256: "rollback-sha",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return handoff
}
