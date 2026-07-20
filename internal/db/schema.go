package db

const schemaSQL = `
CREATE TABLE IF NOT EXISTS repos (
    id             TEXT PRIMARY KEY,
    working_path   TEXT NOT NULL UNIQUE,
    upstream_url   TEXT NOT NULL,
    fork_url       TEXT,
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS runs (
    id                   TEXT PRIMARY KEY,
    repo_id              TEXT NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    branch               TEXT NOT NULL,
    head_sha             TEXT NOT NULL,
    base_sha             TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'pending',
    provisioning_phase   TEXT NOT NULL DEFAULT 'created',
    provisioning_progress INTEGER NOT NULL DEFAULT 0,
    provisioning_error   TEXT,
    provisioning_started_at INTEGER,
    provisioning_completed_at INTEGER,
    pr_url               TEXT,
    error                TEXT,
    blocked_reason       TEXT,
    awaiting_agent_since INTEGER,
    parked_ms            INTEGER,
    requires_github_publication_profile INTEGER NOT NULL DEFAULT 0,
    requires_codex_state_root INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS step_results (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_name        TEXT NOT NULL,
    step_order       INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    exit_code        INTEGER,
    duration_ms      INTEGER,
    log_path         TEXT,
    findings_json    TEXT,
    error            TEXT,
    started_at       INTEGER,
    completed_at     INTEGER,
    last_activity_at INTEGER,
    last_activity    TEXT,
    agent_pid        INTEGER,
    auto_fix_limit   INTEGER
);

CREATE TABLE IF NOT EXISTS step_rounds (
    id                   TEXT PRIMARY KEY,
    step_result_id       TEXT NOT NULL REFERENCES step_results(id) ON DELETE CASCADE,
    round                INTEGER NOT NULL,
    trigger_type         TEXT NOT NULL,
    findings_json        TEXT,
    user_findings_json   TEXT,
    selected_finding_ids TEXT,
    selection_source     TEXT,
    fix_summary          TEXT,
    duration_ms          INTEGER NOT NULL,
    created_at           INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_invocations (
    id                    TEXT PRIMARY KEY,
    run_id                TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_name             TEXT NOT NULL,
    round                 INTEGER NOT NULL,
    purpose               TEXT NOT NULL,
    agent                 TEXT NOT NULL,
    model                 TEXT,
    model_provider        TEXT,
    session_mode          TEXT NOT NULL,
    session_key           TEXT,
    fallback_reason       TEXT,
    started_at            INTEGER NOT NULL,
    completed_at          INTEGER NOT NULL,
    duration_ms           INTEGER NOT NULL,
    subprocess_wait_ms    INTEGER,
    exit_status           TEXT NOT NULL,
    failure_category      TEXT,
    input_tokens          INTEGER,
    output_tokens         INTEGER,
    cache_read_tokens     INTEGER,
    cache_creation_tokens INTEGER,
    fresh_input_tokens    INTEGER,
    reasoning_tokens      INTEGER,
    delta_input_tokens    INTEGER,
    delta_output_tokens   INTEGER,
    delta_cache_read_tokens INTEGER,
    model_roundtrips      INTEGER,
    tool_calls            INTEGER,
    tool_wait_calls       INTEGER,
    tool_test_lint_calls  INTEGER,
    tool_edit_calls       INTEGER,
    tool_read_calls       INTEGER,
    tool_git_calls        INTEGER,
    tool_other_calls      INTEGER,
    workload_files        INTEGER,
    workload_lines        INTEGER,
    finding_count         INTEGER,
    requested_harness     TEXT,
    effective_harness     TEXT,
    requested_model       TEXT,
    effective_model       TEXT,
    requested_effort      TEXT,
    effective_effort      TEXT,
    route_policy_version  TEXT,
    route_phase           TEXT,
    route_reason          TEXT,
    route_source_config   TEXT,
    route_generation      TEXT
);

CREATE INDEX IF NOT EXISTS idx_agent_invocations_run_started_id
    ON agent_invocations (run_id, started_at, id);

CREATE TABLE IF NOT EXISTS run_agent_sessions (
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    agent      TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (run_id, role)
);

CREATE TABLE IF NOT EXISTS intent_cache (
    cache_key   TEXT PRIMARY KEY,
    summary     TEXT NOT NULL,
    agent_name  TEXT NOT NULL,
    session_id  TEXT NOT NULL,
    created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS lifecycle_events (
    id          TEXT PRIMARY KEY,
    run_id      TEXT,
    step_name   TEXT,
    event_type  TEXT NOT NULL,
    status      TEXT,
    error       TEXT,
    metadata    TEXT,
    created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_lifecycle_events_run_created
    ON lifecycle_events (run_id, created_at, id);

CREATE TABLE IF NOT EXISTS route_decisions (
    id                    TEXT PRIMARY KEY,
    run_id                TEXT NOT NULL,
    step_name             TEXT,
    round                 INTEGER NOT NULL,
    requested_harness     TEXT NOT NULL,
    effective_harness     TEXT NOT NULL,
    requested_model       TEXT,
    effective_model       TEXT,
    requested_effort      TEXT,
    effective_effort      TEXT,
    policy_version        TEXT NOT NULL,
    phase                 TEXT NOT NULL,
    risk                  TEXT NOT NULL DEFAULT 'unknown',
    reason                TEXT NOT NULL,
    source_configuration  TEXT,
    configuration_generation TEXT,
    repository            TEXT,
    prompt_sha256         TEXT,
    prompt_bytes          INTEGER NOT NULL DEFAULT 0,
    prompt_transport     TEXT NOT NULL DEFAULT 'stdin',
    created_at            INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_route_decisions_run_created
    ON route_decisions (run_id, created_at, id);

CREATE TABLE IF NOT EXISTS route_results (
    id         TEXT PRIMARY KEY,
    run_id     TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_name  TEXT NOT NULL,
    round      INTEGER NOT NULL,
    phase      TEXT NOT NULL,
    risk       TEXT NOT NULL DEFAULT 'unknown',
    append_seq INTEGER NOT NULL CHECK (append_seq > 0),
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_route_results_run_created
    ON route_results (run_id, created_at, id);

-- One row is the durable append-order authority for completed route results.
-- It is advanced and consumed in the same transaction as each result insert.
CREATE TABLE IF NOT EXISTS route_result_sequence (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    next_seq INTEGER NOT NULL
);

-- The daemon remains a single coordinator. These rows are only the durable
-- handshake and evidence needed to quiesce that coordinator at safe pipeline
-- boundaries before replacing it; they do not authorize concurrent daemons.
CREATE TABLE IF NOT EXISTS daemon_generation (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    generation        TEXT NOT NULL,
    build             TEXT NOT NULL,
    protocol_version  INTEGER NOT NULL,
    schema_version    INTEGER NOT NULL,
    maintenance_phase TEXT NOT NULL DEFAULT 'active',
    updated_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS daemon_handoffs (
    id                  TEXT PRIMARY KEY,
    source_generation   TEXT NOT NULL,
    target_build        TEXT NOT NULL,
    target_protocol_min INTEGER NOT NULL,
    target_protocol_max INTEGER NOT NULL,
    target_schema_min   INTEGER NOT NULL,
    target_schema_max   INTEGER NOT NULL,
    target_path         TEXT NOT NULL,
    target_sha256       TEXT NOT NULL,
    rollback_path       TEXT NOT NULL,
    rollback_sha256     TEXT NOT NULL,
    phase               TEXT NOT NULL,
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS daemon_handoff_events (
    id         TEXT PRIMARY KEY,
    handoff_id TEXT NOT NULL REFERENCES daemon_handoffs(id) ON DELETE CASCADE,
    phase      TEXT NOT NULL,
    detail     TEXT,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_daemon_handoff_events_order
    ON daemon_handoff_events (handoff_id, created_at, id);

CREATE TABLE IF NOT EXISTS run_handoff_checkpoints (
    run_id      TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    handoff_id  TEXT NOT NULL,
    generation  TEXT NOT NULL,
    next_step   TEXT NOT NULL,
    worktree    TEXT NOT NULL,
    head_sha    TEXT NOT NULL,
    state       TEXT NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS daemon_admission_queue (
    id                  TEXT PRIMARY KEY,
    handoff_id          TEXT NOT NULL,
    repo_id             TEXT NOT NULL,
    branch              TEXT NOT NULL,
    head_sha            TEXT NOT NULL,
    base_sha            TEXT NOT NULL,
    trigger             TEXT NOT NULL,
    skip_steps_json     TEXT NOT NULL DEFAULT '[]',
    intent              TEXT,
    requires_github_publication_profile INTEGER NOT NULL DEFAULT 0,
    requires_codex_state_root INTEGER NOT NULL DEFAULT 0,
    state               TEXT NOT NULL DEFAULT 'queued',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_daemon_admission_queue_order
    ON daemon_admission_queue (handoff_id, state, created_at, id);
`

// migrationStatements hold additive schema changes applied to databases that
// were created before the referenced columns existed. Each statement must be
// idempotent via its error being tolerated when the column already exists.
var migrationStatements = []string{
	`ALTER TABLE repos ADD COLUMN fork_url TEXT`,
	`ALTER TABLE step_rounds ADD COLUMN selected_finding_ids TEXT`,
	`ALTER TABLE step_rounds ADD COLUMN selection_source TEXT`,
	`ALTER TABLE step_rounds ADD COLUMN fix_summary TEXT`,
	`ALTER TABLE step_rounds ADD COLUMN user_findings_json TEXT`,
	`ALTER TABLE runs ADD COLUMN intent TEXT`,
	`ALTER TABLE runs ADD COLUMN intent_source TEXT`,
	`ALTER TABLE runs ADD COLUMN intent_session_id TEXT`,
	`ALTER TABLE runs ADD COLUMN intent_score REAL`,
	`ALTER TABLE runs ADD COLUMN awaiting_agent_since INTEGER`,
	`ALTER TABLE runs ADD COLUMN parked_ms INTEGER`,
	`ALTER TABLE runs ADD COLUMN requires_github_publication_profile INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN requires_codex_state_root INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN blocked_reason TEXT`,
	`ALTER TABLE runs ADD COLUMN provisioning_phase TEXT NOT NULL DEFAULT 'created'`,
	`ALTER TABLE runs ADD COLUMN provisioning_progress INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE runs ADD COLUMN provisioning_error TEXT`,
	`ALTER TABLE runs ADD COLUMN provisioning_started_at INTEGER`,
	`ALTER TABLE runs ADD COLUMN provisioning_completed_at INTEGER`,
	`ALTER TABLE step_results ADD COLUMN last_activity_at INTEGER`,
	`ALTER TABLE step_results ADD COLUMN last_activity TEXT`,
	`ALTER TABLE step_results ADD COLUMN agent_pid INTEGER`,
	`ALTER TABLE step_results ADD COLUMN auto_fix_limit INTEGER`,
	// Session-fidelity telemetry columns (all nullable so pre-existing rows read
	// back as unknown, never a fabricated zero).
	`ALTER TABLE agent_invocations ADD COLUMN model_provider TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN fallback_reason TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN subprocess_wait_ms INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN fresh_input_tokens INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN reasoning_tokens INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN delta_input_tokens INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN delta_output_tokens INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN delta_cache_read_tokens INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN model_roundtrips INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_wait_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_test_lint_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_edit_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_read_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_git_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN tool_other_calls INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN workload_files INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN workload_lines INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN finding_count INTEGER`,
	`ALTER TABLE agent_invocations ADD COLUMN requested_harness TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN effective_harness TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN requested_model TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN effective_model TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN requested_effort TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN effective_effort TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN route_policy_version TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN route_phase TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN route_reason TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN route_source_config TEXT`,
	`ALTER TABLE agent_invocations ADD COLUMN route_generation TEXT`,
	`ALTER TABLE route_decisions ADD COLUMN prompt_sha256 TEXT`,
	`ALTER TABLE route_decisions ADD COLUMN prompt_bytes INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE route_decisions ADD COLUMN prompt_transport TEXT NOT NULL DEFAULT 'stdin'`,
	`ALTER TABLE route_decisions ADD COLUMN risk TEXT NOT NULL DEFAULT 'unknown'`,
	`ALTER TABLE route_results ADD COLUMN append_seq INTEGER`,
}
