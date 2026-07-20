package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// HandoffPhase is a durable maintenance state. The protocol intentionally
// preserves the existing single-daemon ownership model: a handoff must reach a
// safe checkpoint before the old coordinator exits and the target adopts it.
type HandoffPhase string

const (
	HandoffPhaseActive       HandoffPhase = "active"
	HandoffPhasePreparing    HandoffPhase = "preparing"
	HandoffPhaseQuiescing    HandoffPhase = "quiescing"
	HandoffPhaseCheckpointed HandoffPhase = "checkpointed"
	HandoffPhaseAdopting     HandoffPhase = "adopting"
	HandoffPhaseAdopted      HandoffPhase = "adopted"
	HandoffPhaseRollback     HandoffPhase = "rollback"
	HandoffPhaseFailed       HandoffPhase = "failed"
	HandoffPhaseRetired      HandoffPhase = "retired"
)

const (
	CheckpointStateParked  = "parked"
	CheckpointStateClaimed = "claimed"
)

type DaemonGeneration struct {
	Generation       string
	Build            string
	Protocol         int
	Schema           int
	MaintenancePhase HandoffPhase
	UpdatedAt        int64
}

type HandoffSpec struct {
	SourceGeneration  string
	TargetBuild       string
	TargetProtocolMin int
	TargetProtocolMax int
	TargetSchemaMin   int
	TargetSchemaMax   int
	TargetPath        string
	TargetSHA256      string
	RollbackPath      string
	RollbackSHA256    string
}

type Handoff struct {
	ID string
	HandoffSpec
	Phase     HandoffPhase
	CreatedAt int64
	UpdatedAt int64
}

type HandoffEvent struct {
	ID        string
	HandoffID string
	Phase     HandoffPhase
	Detail    string
	CreatedAt int64
}

type RunHandoffCheckpoint struct {
	RunID      string
	HandoffID  string
	Generation string
	NextStep   string
	Worktree   string
	HeadSHA    string
	State      string
	CreatedAt  int64
	UpdatedAt  int64
}

type DaemonAdmission struct {
	ID                               string
	HandoffID                        string
	RepoID                           string
	Branch                           string
	HeadSHA                          string
	BaseSHA                          string
	Trigger                          string
	SkipSteps                        []string
	Intent                           string
	RequiresGitHubPublicationProfile bool
	RequiresCodexStateRoot           bool
	State                            string
	CreatedAt                        int64
	UpdatedAt                        int64
}

func (d *DB) RegisterDaemonGeneration(g DaemonGeneration) error {
	if strings.TrimSpace(g.Generation) == "" || strings.TrimSpace(g.Build) == "" || g.Protocol < 1 || g.Schema < 1 {
		return fmt.Errorf("daemon generation metadata is incomplete")
	}
	phase := g.MaintenancePhase
	if phase == "" {
		phase = HandoffPhaseActive
	}
	_, err := d.sql.Exec(`INSERT INTO daemon_generation
		(id, generation, build, protocol_version, schema_version, maintenance_phase, updated_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET generation = excluded.generation,
		build = excluded.build, protocol_version = excluded.protocol_version,
		schema_version = excluded.schema_version, maintenance_phase = excluded.maintenance_phase,
		updated_at = excluded.updated_at`,
		g.Generation, g.Build, g.Protocol, g.Schema, phase, now())
	if err != nil {
		return fmt.Errorf("register daemon generation: %w", err)
	}
	return nil
}

func (d *DB) GetDaemonGeneration() (*DaemonGeneration, error) {
	var g DaemonGeneration
	err := d.sql.QueryRow(`SELECT generation, build, protocol_version, schema_version, maintenance_phase, updated_at
		FROM daemon_generation WHERE id = 1`).Scan(&g.Generation, &g.Build, &g.Protocol, &g.Schema, &g.MaintenancePhase, &g.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get daemon generation: %w", err)
	}
	return &g, nil
}

func (d *DB) BeginHandoff(spec HandoffSpec, currentProtocol, currentSchema int) (*Handoff, error) {
	if currentProtocol < spec.TargetProtocolMin || currentProtocol > spec.TargetProtocolMax {
		return nil, fmt.Errorf("target protocol range %d..%d is incompatible with protocol %d", spec.TargetProtocolMin, spec.TargetProtocolMax, currentProtocol)
	}
	if currentSchema < spec.TargetSchemaMin || currentSchema > spec.TargetSchemaMax {
		return nil, fmt.Errorf("target schema range %d..%d is incompatible with schema %d", spec.TargetSchemaMin, spec.TargetSchemaMax, currentSchema)
	}
	if strings.TrimSpace(spec.SourceGeneration) == "" || strings.TrimSpace(spec.TargetBuild) == "" {
		return nil, fmt.Errorf("handoff generation/build metadata is incomplete")
	}
	if !filepath.IsAbs(spec.TargetPath) || !filepath.IsAbs(spec.RollbackPath) || strings.TrimSpace(spec.TargetSHA256) == "" || strings.TrimSpace(spec.RollbackSHA256) == "" {
		return nil, fmt.Errorf("handoff artifacts must have absolute paths and exact hashes")
	}
	generation, err := d.GetDaemonGeneration()
	if err != nil {
		return nil, err
	}
	if generation == nil || generation.Generation != spec.SourceGeneration || generation.Protocol != currentProtocol || generation.Schema != currentSchema {
		return nil, fmt.Errorf("handoff source does not match the registered daemon generation")
	}
	if existing, err := d.CurrentHandoff(); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("handoff %s is still %s", existing.ID, existing.Phase)
	}

	ts := now()
	h := &Handoff{ID: newID(), HandoffSpec: spec, Phase: HandoffPhasePreparing, CreatedAt: ts, UpdatedAt: ts}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin handoff: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO daemon_handoffs
		(id, source_generation, target_build, target_protocol_min, target_protocol_max,
		target_schema_min, target_schema_max, target_path, target_sha256,
		rollback_path, rollback_sha256, phase, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.ID, spec.SourceGeneration, spec.TargetBuild, spec.TargetProtocolMin, spec.TargetProtocolMax,
		spec.TargetSchemaMin, spec.TargetSchemaMax, spec.TargetPath, spec.TargetSHA256,
		spec.RollbackPath, spec.RollbackSHA256, h.Phase, ts, ts); err != nil {
		return nil, fmt.Errorf("insert handoff: %w", err)
	}
	if err := insertHandoffEvent(tx, h.ID, h.Phase, "handoff prepared", ts); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE daemon_generation SET maintenance_phase = ?, updated_at = ? WHERE id = 1`, h.Phase, ts); err != nil {
		return nil, fmt.Errorf("update daemon maintenance phase: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit handoff: %w", err)
	}
	return h, nil
}

func (d *DB) CurrentHandoff() (*Handoff, error) {
	row := d.sql.QueryRow(`SELECT id, source_generation, target_build, target_protocol_min,
		target_protocol_max, target_schema_min, target_schema_max, target_path,
		target_sha256, rollback_path, rollback_sha256, phase, created_at, updated_at
		FROM daemon_handoffs WHERE phase NOT IN (?, ?) ORDER BY created_at DESC, id DESC LIMIT 1`, HandoffPhaseFailed, HandoffPhaseRetired)
	return scanHandoff(row)
}

func (d *DB) GetHandoff(id string) (*Handoff, error) {
	return scanHandoff(d.sql.QueryRow(`SELECT id, source_generation, target_build, target_protocol_min,
		target_protocol_max, target_schema_min, target_schema_max, target_path,
		target_sha256, rollback_path, rollback_sha256, phase, created_at, updated_at
		FROM daemon_handoffs WHERE id = ?`, id))
}

func scanHandoff(row *sql.Row) (*Handoff, error) {
	var h Handoff
	err := row.Scan(&h.ID, &h.SourceGeneration, &h.TargetBuild, &h.TargetProtocolMin,
		&h.TargetProtocolMax, &h.TargetSchemaMin, &h.TargetSchemaMax, &h.TargetPath,
		&h.TargetSHA256, &h.RollbackPath, &h.RollbackSHA256, &h.Phase, &h.CreatedAt, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get handoff: %w", err)
	}
	return &h, nil
}

func (d *DB) TransitionHandoff(id string, next HandoffPhase, detail string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin handoff transition: %w", err)
	}
	defer tx.Rollback()
	var current HandoffPhase
	if err := tx.QueryRow(`SELECT phase FROM daemon_handoffs WHERE id = ?`, id).Scan(&current); err != nil {
		return fmt.Errorf("read handoff phase: %w", err)
	}
	if !validHandoffTransition(current, next) {
		return fmt.Errorf("refusing handoff transition %s -> %s", current, next)
	}
	if next == HandoffPhaseAdopting {
		var queued int
		if err := tx.QueryRow(`SELECT count(*) FROM daemon_admission_queue WHERE handoff_id = ? AND state = 'queued'`, id).Scan(&queued); err != nil {
			return fmt.Errorf("inspect queued admissions before adoption: %w", err)
		}
		if queued > 0 {
			return fmt.Errorf("refusing target adoption with %d queued daemon admissions", queued)
		}
	}
	ts := now()
	if _, err := tx.Exec(`UPDATE daemon_handoffs SET phase = ?, updated_at = ? WHERE id = ?`, next, ts, id); err != nil {
		return fmt.Errorf("update handoff phase: %w", err)
	}
	if err := insertHandoffEvent(tx, id, next, detail, ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE daemon_generation SET maintenance_phase = ?, updated_at = ? WHERE id = 1`, next, ts); err != nil {
		return fmt.Errorf("update daemon maintenance phase: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit handoff transition: %w", err)
	}
	return nil
}

func validHandoffTransition(current, next HandoffPhase) bool {
	switch current {
	case HandoffPhasePreparing:
		return next == HandoffPhaseQuiescing || next == HandoffPhaseRollback || next == HandoffPhaseFailed
	case HandoffPhaseQuiescing:
		return next == HandoffPhaseCheckpointed || next == HandoffPhaseRollback || next == HandoffPhaseFailed
	case HandoffPhaseCheckpointed:
		return next == HandoffPhaseAdopting || next == HandoffPhaseRollback || next == HandoffPhaseFailed
	case HandoffPhaseAdopting:
		return next == HandoffPhaseAdopted || next == HandoffPhaseRollback || next == HandoffPhaseFailed
	case HandoffPhaseAdopted, HandoffPhaseRollback:
		return next == HandoffPhaseRetired
	default:
		return false
	}
}

func insertHandoffEvent(tx *sql.Tx, handoffID string, phase HandoffPhase, detail string, ts int64) error {
	if _, err := tx.Exec(`INSERT INTO daemon_handoff_events (id, handoff_id, phase, detail, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), handoffID, phase, nullableString(detail), ts); err != nil {
		return fmt.Errorf("append handoff event: %w", err)
	}
	return nil
}

func (d *DB) HandoffEvents(handoffID string) ([]HandoffEvent, error) {
	rows, err := d.sql.Query(`SELECT id, handoff_id, phase, COALESCE(detail, ''), created_at
		FROM daemon_handoff_events WHERE handoff_id = ? ORDER BY created_at, id`, handoffID)
	if err != nil {
		return nil, fmt.Errorf("list handoff events: %w", err)
	}
	defer rows.Close()
	var events []HandoffEvent
	for rows.Next() {
		var event HandoffEvent
		if err := rows.Scan(&event.ID, &event.HandoffID, &event.Phase, &event.Detail, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan handoff event: %w", err)
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (d *DB) UpsertRunHandoffCheckpoint(checkpoint RunHandoffCheckpoint) error {
	if strings.TrimSpace(checkpoint.RunID) == "" || strings.TrimSpace(checkpoint.HandoffID) == "" || strings.TrimSpace(checkpoint.Generation) == "" || strings.TrimSpace(checkpoint.NextStep) == "" || strings.TrimSpace(checkpoint.HeadSHA) == "" || !filepath.IsAbs(checkpoint.Worktree) {
		return fmt.Errorf("run handoff checkpoint is incomplete")
	}
	if checkpoint.State != CheckpointStateParked && checkpoint.State != CheckpointStateClaimed {
		return fmt.Errorf("invalid run handoff checkpoint state %q", checkpoint.State)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return fmt.Errorf("begin run handoff checkpoint: %w", err)
	}
	defer tx.Rollback()
	var sourceGeneration string
	var handoffPhase HandoffPhase
	if err := tx.QueryRow(`SELECT source_generation, phase FROM daemon_handoffs WHERE id = ?`, checkpoint.HandoffID).Scan(&sourceGeneration, &handoffPhase); err != nil {
		return fmt.Errorf("validate run handoff checkpoint: %w", err)
	}
	if sourceGeneration != checkpoint.Generation {
		return fmt.Errorf("run handoff checkpoint generation does not match its handoff")
	}
	if checkpoint.State == CheckpointStateParked && handoffPhase != HandoffPhaseQuiescing && handoffPhase != HandoffPhaseCheckpointed {
		return fmt.Errorf("cannot park run while handoff is %s", handoffPhase)
	}
	if checkpoint.State == CheckpointStateClaimed && handoffPhase != HandoffPhaseAdopted {
		return fmt.Errorf("cannot claim run before target adoption")
	}
	var existing RunHandoffCheckpoint
	existingErr := tx.QueryRow(`SELECT run_id, handoff_id, generation, next_step, worktree, head_sha, state, created_at, updated_at
		FROM run_handoff_checkpoints WHERE run_id = ?`, checkpoint.RunID).Scan(
		&existing.RunID, &existing.HandoffID, &existing.Generation, &existing.NextStep,
		&existing.Worktree, &existing.HeadSHA, &existing.State, &existing.CreatedAt, &existing.UpdatedAt)
	if existingErr != nil && existingErr != sql.ErrNoRows {
		return fmt.Errorf("read existing run handoff checkpoint: %w", existingErr)
	}
	if existingErr == nil {
		if existing.HandoffID != checkpoint.HandoffID || existing.Generation != checkpoint.Generation ||
			existing.NextStep != checkpoint.NextStep || filepath.Clean(existing.Worktree) != filepath.Clean(checkpoint.Worktree) || existing.HeadSHA != checkpoint.HeadSHA {
			return fmt.Errorf("run handoff checkpoint evidence is immutable")
		}
		if existing.State == CheckpointStateClaimed && checkpoint.State == CheckpointStateParked {
			return fmt.Errorf("claimed run handoff checkpoint cannot return to parked")
		}
	}
	ts := now()
	_, err = tx.Exec(`INSERT INTO run_handoff_checkpoints
		(run_id, handoff_id, generation, next_step, worktree, head_sha, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET handoff_id = excluded.handoff_id,
		generation = excluded.generation, next_step = excluded.next_step,
		worktree = excluded.worktree, head_sha = excluded.head_sha,
		state = excluded.state, updated_at = excluded.updated_at`,
		checkpoint.RunID, checkpoint.HandoffID, checkpoint.Generation, checkpoint.NextStep,
		checkpoint.Worktree, checkpoint.HeadSHA, checkpoint.State, ts, ts)
	if err != nil {
		return fmt.Errorf("upsert run handoff checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit run handoff checkpoint: %w", err)
	}
	return nil
}

func (d *DB) GetRunHandoffCheckpoint(runID string) (*RunHandoffCheckpoint, error) {
	var checkpoint RunHandoffCheckpoint
	err := d.sql.QueryRow(`SELECT run_id, handoff_id, generation, next_step, worktree, head_sha, state, created_at, updated_at
		FROM run_handoff_checkpoints WHERE run_id = ?`, runID).Scan(
		&checkpoint.RunID, &checkpoint.HandoffID, &checkpoint.Generation, &checkpoint.NextStep,
		&checkpoint.Worktree, &checkpoint.HeadSHA, &checkpoint.State, &checkpoint.CreatedAt, &checkpoint.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run handoff checkpoint: %w", err)
	}
	return &checkpoint, nil
}

func (d *DB) DeleteRunHandoffCheckpoint(runID string) error {
	if _, err := d.sql.Exec(`DELETE FROM run_handoff_checkpoints WHERE run_id = ?`, runID); err != nil {
		return fmt.Errorf("delete run handoff checkpoint: %w", err)
	}
	return nil
}

func (d *DB) EnqueueDaemonAdmission(admission DaemonAdmission) (*DaemonAdmission, error) {
	if strings.TrimSpace(admission.HandoffID) == "" || strings.TrimSpace(admission.RepoID) == "" || strings.TrimSpace(admission.Branch) == "" || strings.TrimSpace(admission.HeadSHA) == "" || strings.TrimSpace(admission.Trigger) == "" {
		return nil, fmt.Errorf("daemon admission metadata is incomplete")
	}
	skipJSON, err := json.Marshal(admission.SkipSteps)
	if err != nil {
		return nil, fmt.Errorf("encode queued skip steps: %w", err)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin queued daemon admission: %w", err)
	}
	defer tx.Rollback()
	var phase HandoffPhase
	if err := tx.QueryRow(`SELECT phase FROM daemon_handoffs WHERE id = ?`, admission.HandoffID).Scan(&phase); err != nil {
		return nil, fmt.Errorf("validate queued daemon admission: %w", err)
	}
	if phase != HandoffPhaseQuiescing && phase != HandoffPhaseCheckpointed {
		return nil, fmt.Errorf("cannot queue daemon admission while handoff is %s", phase)
	}
	admission.ID = newID()
	admission.State = "queued"
	admission.CreatedAt = now()
	admission.UpdatedAt = admission.CreatedAt
	_, err = tx.Exec(`INSERT INTO daemon_admission_queue
		(id, handoff_id, repo_id, branch, head_sha, base_sha, trigger, skip_steps_json,
		intent, requires_github_publication_profile, requires_codex_state_root, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		admission.ID, admission.HandoffID, admission.RepoID, admission.Branch,
		admission.HeadSHA, admission.BaseSHA, admission.Trigger, string(skipJSON),
		nullableString(admission.Intent), admission.RequiresGitHubPublicationProfile,
		admission.RequiresCodexStateRoot, admission.State, admission.CreatedAt, admission.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("enqueue daemon admission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit queued daemon admission: %w", err)
	}
	return &admission, nil
}

func (d *DB) QueuedDaemonAdmissions(handoffID string) ([]DaemonAdmission, error) {
	rows, err := d.sql.Query(`SELECT id, handoff_id, repo_id, branch, head_sha, base_sha,
		trigger, skip_steps_json, COALESCE(intent, ''), requires_github_publication_profile,
		requires_codex_state_root, state, created_at, updated_at
		FROM daemon_admission_queue WHERE handoff_id = ? AND state = 'queued'
		ORDER BY created_at, id`, handoffID)
	if err != nil {
		return nil, fmt.Errorf("list queued daemon admissions: %w", err)
	}
	defer rows.Close()
	var admissions []DaemonAdmission
	for rows.Next() {
		var admission DaemonAdmission
		var skipJSON string
		if err := rows.Scan(&admission.ID, &admission.HandoffID, &admission.RepoID,
			&admission.Branch, &admission.HeadSHA, &admission.BaseSHA, &admission.Trigger,
			&skipJSON, &admission.Intent, &admission.RequiresGitHubPublicationProfile,
			&admission.RequiresCodexStateRoot, &admission.State, &admission.CreatedAt,
			&admission.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan queued daemon admission: %w", err)
		}
		if err := json.Unmarshal([]byte(skipJSON), &admission.SkipSteps); err != nil {
			return nil, fmt.Errorf("decode queued skip steps: %w", err)
		}
		admissions = append(admissions, admission)
	}
	return admissions, rows.Err()
}
