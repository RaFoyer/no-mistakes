package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutorParksOnlyAfterCompletedStepAndResumesAtNextStep(t *testing.T) {
	database, p, run, repo := setupTest(t)
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
	if err := database.TransitionHandoff(handoff.ID, db.HandoffPhaseQuiescing, "test quiesce"); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	first := newPassStep(types.StepReview)
	second := newPassStep(types.StepTest)
	exec := NewExecutor(database, p, nil, nil, []Step{first, second}, nil)
	exec.SetHandoffCheckpointer(func(request HandoffCheckpointRequest) (bool, error) {
		if request.NextStep != types.StepTest {
			t.Fatalf("next step = %q, want %q", request.NextStep, types.StepTest)
		}
		if err := database.UpsertRunHandoffCheckpoint(db.RunHandoffCheckpoint{
			RunID: request.Run.ID, HandoffID: handoff.ID, Generation: "generation-a",
			NextStep: string(request.NextStep), Worktree: request.WorkDir,
			HeadSHA: "checkpoint-head", State: db.CheckpointStateParked,
		}); err != nil {
			return false, err
		}
		return true, nil
	})

	err = exec.Execute(context.Background(), run, repo, workDir)
	if !errors.Is(err, ErrHandoffParked) {
		t.Fatalf("Execute error = %v, want ErrHandoffParked", err)
	}
	if first.callCount() != 1 || second.callCount() != 0 {
		t.Fatalf("step calls before resume = first:%d second:%d", first.callCount(), second.callCount())
	}
	results, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != types.StepStatusCompleted || results[1].Status != types.StepStatusPending {
		t.Fatalf("step results at checkpoint = %+v", results)
	}
	stored, err := database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != types.RunRunning || stored.Error != nil {
		t.Fatalf("checkpoint changed run to terminal state: %+v", stored)
	}

	checkpoint, err := database.GetRunHandoffCheckpoint(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []db.HandoffPhase{db.HandoffPhaseCheckpointed, db.HandoffPhaseAdopting, db.HandoffPhaseAdopted} {
		if err := database.TransitionHandoff(handoff.ID, phase, "test transition"); err != nil {
			t.Fatal(err)
		}
	}
	resumed := NewExecutor(database, p, nil, nil, []Step{first, second}, nil)
	if err := resumed.ResumeHandoff(context.Background(), stored, repo, workDir, checkpoint); err != nil {
		t.Fatalf("ResumeHandoff: %v", err)
	}
	if first.callCount() != 1 || second.callCount() != 1 {
		t.Fatalf("step calls after resume = first:%d second:%d", first.callCount(), second.callCount())
	}
	stored, err = database.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != types.RunCompleted {
		t.Fatalf("resumed run status = %q, want completed", stored.Status)
	}
	if checkpoint, err := database.GetRunHandoffCheckpoint(run.ID); err != nil {
		t.Fatal(err)
	} else if checkpoint != nil {
		t.Fatalf("completed resumed run retained checkpoint: %+v", checkpoint)
	}
}

func TestValidateHandoffCheckpointRejectsAmbiguousStepOrOwnedAgent(t *testing.T) {
	database, _, run, _ := setupTest(t)
	steps := []Step{newPassStep(types.StepReview), newPassStep(types.StepTest)}
	for _, step := range steps {
		if _, err := database.InsertStepResult(run.ID, step.Name()); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := &db.RunHandoffCheckpoint{RunID: run.ID, NextStep: string(types.StepTest), State: db.CheckpointStateParked}
	if err := ValidateHandoffCheckpoint(database, run, steps, checkpoint); err == nil {
		t.Fatal("pending first step should make checkpoint ambiguous")
	}

	results, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteStepWithStatus(results[0].ID, types.StepStatusCompleted, 0, 0, ""); err != nil {
		t.Fatal(err)
	}
	pid := 1234
	if err := database.SetStepAgentActivity(results[0].ID, "stale agent", &pid); err != nil {
		t.Fatal(err)
	}
	if err := ValidateHandoffCheckpoint(database, run, steps, checkpoint); err == nil {
		t.Fatal("checkpoint with an owned agent pid should fail closed")
	}
}
