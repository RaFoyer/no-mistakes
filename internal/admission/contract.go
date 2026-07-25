// Package admission defines the source-only contract for an independently
// operated shared-runtime admission coordinator. It contains no endpoint,
// credentials, deployment, local ledger, or production fallback.
package admission

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrCoordinatorUnavailable = errors.New("shared-runtime coordinator is not configured")
	ErrAdmissionClosed        = errors.New("runtime admission is closed")
	ErrInvalidSnapshot        = errors.New("active-set snapshot is invalid")
	ErrInvalidSignature       = errors.New("claim signature is invalid")
	ErrClaimScope             = errors.New("claim does not match runtime scope")
	ErrClaimFuture            = errors.New("claim is from the future")
	ErrClaimExpired           = errors.New("claim is expired")
	ErrClaimReplay            = errors.New("claim has already been used")
	ErrLedgerConflict         = errors.New("claim predecessor does not match ledger tip")
	ErrActiveSetChanged       = errors.New("active set changed before start")
	ErrSupersessionScope      = errors.New("supersession target does not match the active set")
	ErrDelayedStart           = errors.New("claim start window elapsed")
	ErrInvalidTransition      = errors.New("invalid admission transition")
	ErrUnknownLease           = errors.New("unknown admission lease")
	ErrInvalidLease           = errors.New("invalid admission lease")
	ErrClaimAction            = errors.New("claim does not authorize the requested action")
	ErrClaimIssuer            = errors.New("claim does not match the trusted coordinator identity")
)

const (
	DefaultMaxClockSkew  = 30 * time.Second
	CurrentPolicyVersion = "no-mistakes-admission-v2"
)

type Action string

const (
	ActionRunStart       Action = "run-start"
	ActionSupersedeRun   Action = "same-branch-supersession"
	ActionDaemonRecovery Action = "daemon-recovery"
)

func (a Action) Valid() bool {
	switch a {
	case ActionRunStart, ActionSupersedeRun, ActionDaemonRecovery:
		return true
	default:
		return false
	}
}

// ActiveRun is the exact portable representation of an active local run.
// Historical active-set hashes are evidence only and never substitute for a
// fresh Claim and Lease.
type ActiveRun struct {
	ID      string `json:"id"`
	RepoID  string `json:"repo_id"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"head_sha"`
	Status  string `json:"status"`
}

// Supersession identifies the one exact active run that a newer claim may
// replace. It is carried in the signed claim, so a caller cannot broaden a
// same-branch cancellation after admission has been granted.
type Supersession struct {
	Target ActiveRun `json:"target"`
}

func (s Supersession) valid() bool {
	return s.Target.ID != "" && s.Target.RepoID != "" && s.Target.Branch != "" && s.Target.HeadSHA != "" && s.Target.Status != ""
}

func (s Supersession) validate(snapshot Snapshot) error {
	if !s.valid() || len(snapshot.Runs) != 1 || snapshot.Runs[0] != s.Target {
		return ErrSupersessionScope
	}
	return nil
}

// Snapshot binds a runtime scope to a canonical, exact active-run set.
type Snapshot struct {
	Runtime string      `json:"runtime"`
	Runs    []ActiveRun `json:"runs"`
	Digest  string      `json:"digest"`
}

func NewSnapshot(runtime string, runs []ActiveRun) Snapshot {
	canonical := canonicalRuns(runs)
	payload, _ := json.Marshal(struct {
		Runtime string      `json:"runtime"`
		Runs    []ActiveRun `json:"runs"`
	}{Runtime: runtime, Runs: canonical})
	sum := sha256.Sum256(payload)
	return Snapshot{Runtime: runtime, Runs: canonical, Digest: hex.EncodeToString(sum[:])}
}

func (s Snapshot) Validate() error {
	if strings.TrimSpace(s.Runtime) == "" || strings.TrimSpace(s.Digest) == "" {
		return ErrInvalidSnapshot
	}
	seen := make(map[string]bool, len(s.Runs))
	for _, run := range s.Runs {
		if run.ID == "" || run.RepoID == "" || run.Branch == "" || run.HeadSHA == "" || run.Status == "" || seen[run.ID] {
			return ErrInvalidSnapshot
		}
		seen[run.ID] = true
	}
	if NewSnapshot(s.Runtime, s.Runs).Digest != s.Digest {
		return ErrInvalidSnapshot
	}
	return nil
}

func canonicalRuns(runs []ActiveRun) []ActiveRun {
	canonical := append([]ActiveRun(nil), runs...)
	sort.Slice(canonical, func(i, j int) bool {
		left, right := canonical[i], canonical[j]
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		if left.RepoID != right.RepoID {
			return left.RepoID < right.RepoID
		}
		if left.Branch != right.Branch {
			return left.Branch < right.Branch
		}
		if left.HeadSHA != right.HeadSHA {
			return left.HeadSHA < right.HeadSHA
		}
		return left.Status < right.Status
	})
	return canonical
}

// Claimant is a portable identity. It intentionally excludes UID, hostname,
// local paths, and repository-private task identifiers.
type Claimant struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

func (c Claimant) Valid() bool {
	return c.ID != "" && c.Kind != "" && strings.TrimSpace(c.ID) == c.ID && strings.TrimSpace(c.Kind) == c.Kind
}

// ClaimInput is the signed, portable admission packet. PreviousHash binds a
// fresh claim to the coordinator ledger tip observed by its issuer.
type ClaimInput struct {
	Version            int           `json:"version"`
	ClaimID            string        `json:"claim_id"`
	RequestKey         string        `json:"request_key"`
	Action             Action        `json:"action"`
	Runtime            string        `json:"runtime"`
	Claimant           Claimant      `json:"claimant"`
	SnapshotDigest     string        `json:"snapshot_digest"`
	PreconditionDigest string        `json:"precondition_digest"`
	PolicyVersion      string        `json:"policy_version"`
	CoordinatorID      string        `json:"coordinator_id"`
	KeyID              string        `json:"key_id"`
	PreviousHash       string        `json:"previous_hash"`
	IssuedAt           time.Time     `json:"issued_at"`
	StartBy            time.Time     `json:"start_by"`
	ExpiresAt          time.Time     `json:"expires_at"`
	Supersession       *Supersession `json:"supersession,omitempty"`
}

// Claim is mutable only as an untrusted transport value: every use verifies
// Input against Signature before it can affect coordinator state.
type Claim struct {
	Input     ClaimInput `json:"input"`
	Signature []byte     `json:"signature"`
}

func SignClaim(key ed25519.PrivateKey, input ClaimInput) (Claim, error) {
	if len(key) != ed25519.PrivateKeySize {
		return Claim{}, fmt.Errorf("invalid Ed25519 private key")
	}
	input, err := normalizeClaimInput(input)
	if err != nil {
		return Claim{}, err
	}
	payload, _ := json.Marshal(input)
	return Claim{Input: input, Signature: ed25519.Sign(key, payload)}, nil
}

type VerificationContext struct {
	Runtime       string
	Claimant      Claimant
	Action        Action
	CoordinatorID string
	KeyID         string
	Now           time.Time
	MaxClockSkew  time.Duration
}

func VerifyClaim(claim Claim, key ed25519.PublicKey, expected VerificationContext) error {
	if len(key) != ed25519.PublicKeySize {
		return ErrInvalidSignature
	}
	input, err := normalizeClaimInput(claim.Input)
	if err != nil {
		return ErrInvalidSignature
	}
	payload, _ := json.Marshal(input)
	if !ed25519.Verify(key, payload, claim.Signature) {
		return ErrInvalidSignature
	}
	if input.Runtime != expected.Runtime || input.Claimant != expected.Claimant {
		return ErrClaimScope
	}
	if input.Action != expected.Action {
		return ErrClaimAction
	}
	if input.CoordinatorID != expected.CoordinatorID || input.KeyID != expected.KeyID {
		return ErrClaimIssuer
	}
	skew := expected.MaxClockSkew
	if skew < 0 {
		return ErrClaimFuture
	}
	now := expected.Now.UTC()
	if input.IssuedAt.After(now.Add(skew)) {
		return ErrClaimFuture
	}
	if !input.ExpiresAt.After(now.Add(-skew)) {
		return ErrClaimExpired
	}
	return nil
}

func normalizeClaimInput(input ClaimInput) (ClaimInput, error) {
	if input.Version == 0 {
		input.Version = 2
	}
	input.IssuedAt = input.IssuedAt.UTC()
	input.StartBy = input.StartBy.UTC()
	input.ExpiresAt = input.ExpiresAt.UTC()
	if input.Version != 2 || strings.TrimSpace(input.ClaimID) == "" || strings.TrimSpace(input.RequestKey) == "" || !input.Action.Valid() || strings.TrimSpace(input.Runtime) == "" || !input.Claimant.Valid() || strings.TrimSpace(input.SnapshotDigest) == "" || strings.TrimSpace(input.PreconditionDigest) == "" || strings.TrimSpace(input.PolicyVersion) == "" || strings.TrimSpace(input.CoordinatorID) == "" || strings.TrimSpace(input.KeyID) == "" || input.IssuedAt.IsZero() || input.StartBy.IsZero() || input.ExpiresAt.IsZero() || !input.StartBy.After(input.IssuedAt) || !input.ExpiresAt.After(input.StartBy) || (input.Supersession != nil && !input.Supersession.valid()) {
		return ClaimInput{}, fmt.Errorf("incomplete claim packet")
	}
	if (input.Action == ActionSupersedeRun) != (input.Supersession != nil) {
		return ClaimInput{}, fmt.Errorf("action and supersession disagree")
	}
	if input.Supersession != nil {
		supersession := *input.Supersession
		input.Supersession = &supersession
	}
	return input, nil
}

type Request struct {
	Runtime            string
	Claimant           Claimant
	Snapshot           Snapshot
	Supersession       *Supersession
	Action             Action
	PreconditionDigest string
	PolicyVersion      string
	RequestKey         string
}

func normalizeRequest(request Request) (Request, error) {
	if err := request.Snapshot.Validate(); err != nil || request.Runtime == "" || !request.Claimant.Valid() || request.Snapshot.Runtime != request.Runtime {
		return Request{}, ErrClaimScope
	}
	if request.Supersession != nil {
		if err := request.Supersession.validate(request.Snapshot); err != nil {
			return Request{}, err
		}
		supersession := *request.Supersession
		request.Supersession = &supersession
	}
	if request.Action == "" {
		request.Action = ActionRunStart
		if request.Supersession != nil {
			request.Action = ActionSupersedeRun
		}
	}
	if request.PreconditionDigest == "" {
		request.PreconditionDigest = request.Snapshot.Digest
	}
	if request.PolicyVersion == "" {
		request.PolicyVersion = CurrentPolicyVersion
	}
	if !request.Action.Valid() || request.PreconditionDigest == "" || request.PolicyVersion == "" || (request.Action == ActionSupersedeRun) != (request.Supersession != nil) {
		return Request{}, ErrClaimAction
	}
	return request, nil
}

type State string

const (
	StatePrepared  State = "prepared"
	StateStarted   State = "started"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateAborted   State = "aborted"
)

// Lease is a bounded, immutable coordinator-issued token. Its ledger anchor
// lets consumers correlate a run without making the local copy authoritative.
type Lease struct {
	ClaimID            string
	RequestKey         string
	LeaseID            string
	Action             Action
	Runtime            string
	Claimant           Claimant
	SnapshotHash       string
	PreconditionDigest string
	PolicyVersion      string
	CoordinatorID      string
	KeyID              string
	Generation         uint64
	IssuedAt           time.Time
	StartBy            time.Time
	ExpiresAt          time.Time
	LedgerSeq          uint64
	LedgerHash         string
	Supersession       *Supersession
}

func (l Lease) Immutable() bool {
	return l.ClaimID != "" && l.RequestKey != "" && l.LeaseID != "" && l.Action.Valid() && l.Runtime != "" && l.Claimant.Valid() && l.SnapshotHash != "" && l.PreconditionDigest != "" && l.PolicyVersion != "" && l.CoordinatorID != "" && l.KeyID != "" && l.Generation > 0 && !l.IssuedAt.IsZero() && l.StartBy.After(l.IssuedAt) && l.ExpiresAt.After(l.StartBy) && l.LedgerSeq > 0 && l.LedgerHash != "" && (l.Supersession == nil || l.Supersession.valid()) && ((l.Action == ActionSupersedeRun) == (l.Supersession != nil))
}

func (l Lease) ValidateFor(claim Claim, request Request) error {
	request, err := normalizeRequest(request)
	if err != nil || !l.Immutable() {
		return ErrInvalidLease
	}
	input := claim.Input
	// Legacy callers that have not yet adopted caller-stable request keys can
	// still validate the coordinator-issued identity. Production adapters must
	// supply RequestKey before Acquire to gain retry reconciliation semantics.
	if request.RequestKey == "" {
		request.RequestKey = input.RequestKey
	}
	if input.ClaimID != l.ClaimID || input.RequestKey != request.RequestKey || input.RequestKey != l.RequestKey || input.Action != request.Action || input.Action != l.Action || input.Runtime != request.Runtime || input.Runtime != l.Runtime || input.Claimant != request.Claimant || input.Claimant != l.Claimant || input.SnapshotDigest != request.Snapshot.Digest || input.SnapshotDigest != l.SnapshotHash || input.PreconditionDigest != request.PreconditionDigest || input.PreconditionDigest != l.PreconditionDigest || input.PolicyVersion != request.PolicyVersion || input.PolicyVersion != l.PolicyVersion || input.CoordinatorID != l.CoordinatorID || input.KeyID != l.KeyID || !input.IssuedAt.Equal(l.IssuedAt) || !input.StartBy.Equal(l.StartBy) || !input.ExpiresAt.Equal(l.ExpiresAt) || !sameSupersession(input.Supersession, request.Supersession) || !sameSupersession(input.Supersession, l.Supersession) {
		return ErrInvalidLease
	}
	return nil
}

func sameSupersession(left, right *Supersession) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

type LedgerEntry struct {
	Sequence           uint64        `json:"sequence"`
	PriorHash          string        `json:"prior_hash"`
	Hash               string        `json:"hash"`
	ClaimID            string        `json:"claim_id"`
	RequestKey         string        `json:"request_key"`
	LeaseID            string        `json:"lease_id"`
	Action             Action        `json:"action"`
	Runtime            string        `json:"runtime"`
	Claimant           Claimant      `json:"claimant"`
	SnapshotHash       string        `json:"snapshot_hash"`
	PreconditionDigest string        `json:"precondition_digest"`
	PolicyVersion      string        `json:"policy_version"`
	CoordinatorID      string        `json:"coordinator_id"`
	KeyID              string        `json:"key_id"`
	Generation         uint64        `json:"generation"`
	Supersession       *Supersession `json:"supersession,omitempty"`
	State              State         `json:"state"`
	EvidenceDigest     string        `json:"evidence_digest,omitempty"`
	At                 time.Time     `json:"at"`
}

func ledgerHash(entry LedgerEntry) string {
	entry.Hash = ""
	entry.At = entry.At.UTC()
	payload, _ := json.Marshal(entry)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ValidateLedger(entries []LedgerEntry) error {
	prior := ""
	states := make(map[string]State, len(entries))
	claimLeases := make(map[string]string, len(entries))
	requestLeases := make(map[string]string, len(entries))
	type leaseIdentity struct {
		claimID            string
		requestKey         string
		action             Action
		claimant           Claimant
		snapshotHash       string
		preconditionDigest string
		policyVersion      string
		coordinatorID      string
		keyID              string
		generation         uint64
		supersession       string
	}
	identities := make(map[string]leaseIdentity, len(entries))
	runtimeGenerations := make(map[string]uint64)
	for index, entry := range entries {
		if entry.Sequence != uint64(index+1) || entry.PriorHash != prior || entry.Hash != ledgerHash(entry) {
			return fmt.Errorf("ledger entry %d is not hash-linked", index+1)
		}
		if entry.ClaimID == "" || entry.RequestKey == "" || entry.LeaseID == "" || !entry.Action.Valid() || strings.TrimSpace(entry.Runtime) == "" || !entry.Claimant.Valid() || entry.SnapshotHash == "" || entry.PreconditionDigest == "" || entry.PolicyVersion == "" || entry.CoordinatorID == "" || entry.KeyID == "" || entry.Generation == 0 || entry.At.IsZero() {
			return fmt.Errorf("ledger entry %d is incomplete", index+1)
		}
		if (entry.Action == ActionSupersedeRun) != (entry.Supersession != nil) || (entry.Supersession != nil && !entry.Supersession.valid()) {
			return fmt.Errorf("ledger entry %d has inconsistent action scope: %w", index+1, ErrInvalidTransition)
		}
		key := entry.Runtime + "\x00" + entry.LeaseID
		if leaseKey, claimed := claimLeases[entry.ClaimID]; claimed && leaseKey != key {
			return fmt.Errorf("ledger entry %d reuses claim: %w", index+1, ErrClaimReplay)
		}
		if leaseKey, requested := requestLeases[entry.RequestKey]; requested && leaseKey != key {
			return fmt.Errorf("ledger entry %d reuses request key: %w", index+1, ErrClaimReplay)
		}
		state, seen := states[key]
		supersessionJSON, _ := json.Marshal(entry.Supersession)
		identity := leaseIdentity{claimID: entry.ClaimID, requestKey: entry.RequestKey, action: entry.Action, claimant: entry.Claimant, snapshotHash: entry.SnapshotHash, preconditionDigest: entry.PreconditionDigest, policyVersion: entry.PolicyVersion, coordinatorID: entry.CoordinatorID, keyID: entry.KeyID, generation: entry.Generation, supersession: string(supersessionJSON)}
		if priorIdentity, recorded := identities[key]; recorded && priorIdentity != identity {
			return fmt.Errorf("ledger entry %d changes immutable lease fields: %w", index+1, ErrInvalidTransition)
		}
		if !seen && entry.Generation != runtimeGenerations[entry.Runtime]+1 {
			return fmt.Errorf("ledger entry %d changes runtime generation: %w", index+1, ErrInvalidTransition)
		}
		valid := (!seen && entry.State == StatePrepared) ||
			(state == StatePrepared && (entry.State == StateStarted || entry.State == StateAborted)) ||
			(state == StateStarted && (entry.State == StateCompleted || entry.State == StateFailed))
		if !valid {
			return fmt.Errorf("ledger entry %d: %w", index+1, ErrInvalidTransition)
		}
		identities[key] = identity
		if !seen {
			runtimeGenerations[entry.Runtime] = entry.Generation
		}
		claimLeases[entry.ClaimID] = key
		requestLeases[entry.RequestKey] = key
		states[key] = entry.State
		prior = entry.Hash
	}
	return nil
}

type Status struct {
	Runtime         string
	AdmissionClosed bool
	Generation      uint64
	State           State
	Tip             LedgerEntry
	Entries         []LedgerEntry
}

// RuntimeCoordinator is the daemon-facing external trust boundary. Acquire
// returns a signed packet; Prepare atomically closes admission; Start performs
// the immediate active-set CAS. A signed Request supersession may add one
// exact same-branch lease while admission is already closed; terminal
// transitions alone reopen admission.
type RuntimeCoordinator interface {
	Acquire(context.Context, Request) (Claim, error)
	Prepare(context.Context, Claim, Snapshot) (Lease, error)
	Start(context.Context, Lease, Snapshot) error
	Complete(context.Context, Lease, string) error
	Fail(context.Context, Lease, string) error
	Abort(context.Context, Lease, string) error
	Status(context.Context, string) Status
}

// Coordinator is the concise daemon-facing name for RuntimeCoordinator.
type Coordinator = RuntimeCoordinator

// InactiveRuntimeCoordinator is the production default. It deliberately has
// no local fallback: observing active runs is evidence, never authority to
// admit or recover a shared runtime.
type InactiveRuntimeCoordinator struct{}

func NewInactiveRuntimeCoordinator() *InactiveRuntimeCoordinator {
	return &InactiveRuntimeCoordinator{}
}
func (*InactiveRuntimeCoordinator) Acquire(context.Context, Request) (Claim, error) {
	return Claim{}, ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Prepare(context.Context, Claim, Snapshot) (Lease, error) {
	return Lease{}, ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Start(context.Context, Lease, Snapshot) error {
	return ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Complete(context.Context, Lease, string) error {
	return ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Fail(context.Context, Lease, string) error {
	return ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Abort(context.Context, Lease, string) error {
	return ErrCoordinatorUnavailable
}
func (*InactiveRuntimeCoordinator) Status(_ context.Context, runtime string) Status {
	return Status{Runtime: runtime}
}

func NewInactive() *InactiveRuntimeCoordinator { return NewInactiveRuntimeCoordinator() }
