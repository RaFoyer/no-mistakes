package db

import (
	"path/filepath"
	"testing"
)

func TestHandoffLifecycleIsPersistedAppendOnlyAndFailClosed(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	if err := d.RegisterDaemonGeneration(DaemonGeneration{
		Generation: "generation-a",
		Build:      "build-a",
		Protocol:   1,
		Schema:     1,
	}); err != nil {
		t.Fatal(err)
	}
	handoff, err := d.BeginHandoff(HandoffSpec{
		SourceGeneration:  "generation-a",
		TargetBuild:       "build-b",
		TargetProtocolMin: 1,
		TargetProtocolMax: 1,
		TargetSchemaMin:   1,
		TargetSchemaMax:   1,
		TargetPath:        "/immutable/build-b",
		TargetSHA256:      "target-sha",
		RollbackPath:      "/immutable/build-a",
		RollbackSHA256:    "rollback-sha",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Phase != HandoffPhasePreparing {
		t.Fatalf("phase = %q, want preparing", handoff.Phase)
	}

	for _, next := range []HandoffPhase{HandoffPhaseQuiescing, HandoffPhaseCheckpointed, HandoffPhaseAdopting, HandoffPhaseAdopted} {
		if err := d.TransitionHandoff(handoff.ID, next, "test transition"); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseRollback, "too late"); err == nil {
		t.Fatal("automatic rollback after adoption should fail closed")
	}

	events, err := d.HandoffEvents(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("events = %d, want begin plus four transitions", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].CreatedAt < events[i-1].CreatedAt || events[i].ID == events[i-1].ID {
			t.Fatalf("events are not append-only ordered: %+v", events)
		}
	}
}

func TestBeginHandoffRefusesIncompatibleProtocolAndSchema(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	tests := []HandoffSpec{
		{SourceGeneration: "a", TargetBuild: "b", TargetProtocolMin: 2, TargetProtocolMax: 2, TargetSchemaMin: 1, TargetSchemaMax: 1},
		{SourceGeneration: "a", TargetBuild: "b", TargetProtocolMin: 1, TargetProtocolMax: 1, TargetSchemaMin: 2, TargetSchemaMax: 2},
	}
	for _, spec := range tests {
		if _, err := d.BeginHandoff(spec, 1, 1); err == nil {
			t.Fatalf("BeginHandoff(%+v) should refuse incompatible target", spec)
		}
	}
	if current, err := d.CurrentHandoff(); err != nil {
		t.Fatal(err)
	} else if current != nil {
		t.Fatalf("incompatible prepare persisted an active handoff: %+v", current)
	}
}

func TestBeginHandoffRefusesMismatchedRegisteredGeneration(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.RegisterDaemonGeneration(DaemonGeneration{Generation: "generation-a", Build: "build-a", Protocol: 1, Schema: 1}); err != nil {
		t.Fatal(err)
	}
	_, err = d.BeginHandoff(HandoffSpec{
		SourceGeneration: "generation-b", TargetBuild: "build-b",
		TargetProtocolMin: 1, TargetProtocolMax: 1, TargetSchemaMin: 1, TargetSchemaMax: 1,
		TargetPath: "/immutable/build-b", TargetSHA256: "target-sha",
		RollbackPath: "/immutable/build-a", RollbackSHA256: "rollback-sha",
	}, 1, 1)
	if err == nil {
		t.Fatal("handoff prepare should refuse a source other than the registered daemon generation")
	}
	if current, currentErr := d.CurrentHandoff(); currentErr != nil {
		t.Fatal(currentErr)
	} else if current != nil {
		t.Fatalf("mismatched source persisted handoff: %+v", current)
	}
}

func TestRunHandoffCheckpointRoundTripsWithoutChangingRunStatus(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, err := d.InsertRepo("/tmp/project", "git@example.test:project.git", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateRunStatus(run.ID, "running"); err != nil {
		t.Fatal(err)
	}
	handoff := beginMaintenanceTestHandoff(t, d)
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseQuiescing, "test quiesce"); err != nil {
		t.Fatal(err)
	}

	checkpoint := RunHandoffCheckpoint{
		RunID:      run.ID,
		HandoffID:  handoff.ID,
		Generation: "generation-a",
		NextStep:   "review",
		Worktree:   "/tmp/worktree",
		HeadSHA:    "checkpoint-head",
		State:      CheckpointStateParked,
	}
	if err := d.UpsertRunHandoffCheckpoint(checkpoint); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetRunHandoffCheckpoint(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.NextStep != checkpoint.NextStep || got.HeadSHA != checkpoint.HeadSHA || got.State != CheckpointStateParked {
		t.Fatalf("checkpoint = %+v, want %+v", got, checkpoint)
	}
	storedRun, err := d.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedRun.Status != "running" {
		t.Fatalf("run status = %q, checkpoint must not cancel/fail/complete it", storedRun.Status)
	}
	claimed := checkpoint
	claimed.State = CheckpointStateClaimed
	if err := d.UpsertRunHandoffCheckpoint(claimed); err == nil {
		t.Fatal("claim should refuse before target adoption")
	}
	for _, phase := range []HandoffPhase{HandoffPhaseCheckpointed, HandoffPhaseAdopting, HandoffPhaseAdopted} {
		if err := d.TransitionHandoff(handoff.ID, phase, "test transition"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.UpsertRunHandoffCheckpoint(claimed); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertRunHandoffCheckpoint(checkpoint); err == nil {
		t.Fatal("claimed checkpoint should never revert to parked")
	}
	changed := claimed
	changed.HeadSHA = "different-head"
	if err := d.UpsertRunHandoffCheckpoint(changed); err == nil {
		t.Fatal("checkpoint evidence should be immutable")
	}
}

func TestQueuedAdmissionPersistsWithoutPrivateProfileValues(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	handoff := beginMaintenanceTestHandoff(t, d)
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseQuiescing, "test quiesce"); err != nil {
		t.Fatal(err)
	}
	queued, err := d.EnqueueDaemonAdmission(DaemonAdmission{
		HandoffID: handoff.ID, RepoID: "repo-a", Branch: "feature",
		HeadSHA: "head", BaseSHA: "base", Trigger: "push",
		SkipSteps: []string{"review"}, Intent: "bounded intent",
		RequiresGitHubPublicationProfile: true, RequiresCodexStateRoot: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := d.QueuedDaemonAdmissions(handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != queued.ID || !items[0].RequiresGitHubPublicationProfile || !items[0].RequiresCodexStateRoot {
		t.Fatalf("queued admissions = %+v", items)
	}
	if items[0].Intent != "bounded intent" || len(items[0].SkipSteps) != 1 || items[0].SkipSteps[0] != "review" {
		t.Fatalf("queued admission lost non-secret execution metadata: %+v", items[0])
	}
}

func beginMaintenanceTestHandoff(t *testing.T, d *DB) *Handoff {
	t.Helper()
	if err := d.RegisterDaemonGeneration(DaemonGeneration{Generation: "generation-a", Build: "build-a", Protocol: 1, Schema: 1}); err != nil {
		t.Fatal(err)
	}
	handoff, err := d.BeginHandoff(HandoffSpec{
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

func TestQueuedAdmissionBlocksTargetAdoption(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.RegisterDaemonGeneration(DaemonGeneration{Generation: "a", Build: "a", Protocol: 1, Schema: 1}); err != nil {
		t.Fatal(err)
	}
	handoff, err := d.BeginHandoff(HandoffSpec{
		SourceGeneration: "a", TargetBuild: "b", TargetProtocolMin: 1, TargetProtocolMax: 1,
		TargetSchemaMin: 1, TargetSchemaMax: 1, TargetPath: "/immutable/b", TargetSHA256: "b",
		RollbackPath: "/immutable/a", RollbackSHA256: "a",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseQuiescing, "quiesce"); err != nil {
		t.Fatal(err)
	}
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseCheckpointed, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.EnqueueDaemonAdmission(DaemonAdmission{HandoffID: handoff.ID, RepoID: "r", Branch: "b", HeadSHA: "h", Trigger: "push"}); err != nil {
		t.Fatal(err)
	}
	if err := d.TransitionHandoff(handoff.ID, HandoffPhaseAdopting, "unsafe"); err == nil {
		t.Fatal("target adoption should refuse while durable admissions are queued")
	}
}
