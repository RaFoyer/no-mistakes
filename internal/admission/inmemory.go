package admission

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// InMemoryConfig configures deterministic, local adversarial test doubles.
// It is never a production configuration surface.
type InMemoryConfig struct {
	PrivateKey  ed25519.PrivateKey
	Clock       func() time.Time
	NextID      func() string
	StartWindow time.Duration
	LeaseWindow time.Duration
}

type InMemory struct {
	mu                       sync.Mutex
	privateKey               ed25519.PrivateKey
	publicKey                ed25519.PublicKey
	clock                    func() time.Time
	nextID                   func() string
	startWindow, leaseWindow time.Duration
	runtimes                 map[string]*runtimeState
}

type runtimeState struct {
	closed     bool
	generation uint64
	entries    []LedgerEntry
	claims     map[string]State
	leases     map[string]Lease
}

func NewInMemory(config InMemoryConfig) (*InMemory, error) {
	if len(config.PrivateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("in-memory coordinator requires Ed25519 private key")
	}
	if config.Clock == nil {
		config.Clock = func() time.Time { return time.Unix(0, 0).UTC() }
	}
	if config.NextID == nil {
		config.NextID = deterministicIDGenerator()
	}
	if config.StartWindow <= 0 {
		config.StartWindow = 30 * time.Second
	}
	if config.LeaseWindow <= config.StartWindow {
		config.LeaseWindow = 2 * time.Minute
	}
	public := config.PrivateKey.Public().(ed25519.PublicKey)
	return &InMemory{
		privateKey:  append(ed25519.PrivateKey(nil), config.PrivateKey...),
		publicKey:   append(ed25519.PublicKey(nil), public...),
		clock:       config.Clock,
		nextID:      config.NextID,
		startWindow: config.StartWindow,
		leaseWindow: config.LeaseWindow,
		runtimes:    make(map[string]*runtimeState),
	}, nil
}

func deterministicIDGenerator() func() string {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("test-admission-%d", next)
	}
}

func (c *InMemory) now() time.Time { return c.clock().UTC() }

func (c *InMemory) runtime(name string) *runtimeState {
	state := c.runtimes[name]
	if state == nil {
		state = &runtimeState{claims: make(map[string]State), leases: make(map[string]Lease)}
		c.runtimes[name] = state
	}
	return state
}

func (c *InMemory) Acquire(ctx context.Context, request Request) (Claim, error) {
	if err := ctx.Err(); err != nil {
		return Claim{}, err
	}
	if err := request.Snapshot.Validate(); err != nil || request.Runtime == "" || !request.Claimant.Valid() || request.Snapshot.Runtime != request.Runtime {
		return Claim{}, ErrClaimScope
	}
	if request.Supersession != nil {
		if err := request.Supersession.validate(request.Snapshot); err != nil {
			return Claim{}, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.runtime(request.Runtime)
	if state.closed && (request.Supersession == nil || !canSupersede(state)) {
		return Claim{}, ErrAdmissionClosed
	}
	previous := ""
	if len(state.entries) != 0 {
		previous = state.entries[len(state.entries)-1].Hash
	}
	now := c.now()
	return SignClaim(c.privateKey, ClaimInput{
		ClaimID:        c.nextID(),
		Runtime:        request.Runtime,
		Claimant:       request.Claimant,
		SnapshotDigest: request.Snapshot.Digest,
		PreviousHash:   previous,
		IssuedAt:       now,
		StartBy:        now.Add(c.startWindow),
		ExpiresAt:      now.Add(c.leaseWindow),
		Supersession:   request.Supersession,
	})
}

func (c *InMemory) Prepare(ctx context.Context, claim Claim, snapshot Snapshot) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return Lease{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	input := claim.Input
	if err := VerifyClaim(claim, c.publicKey, snapshot.Runtime, input.Claimant, c.now()); err != nil {
		return Lease{}, err
	}
	if input.SnapshotDigest != snapshot.Digest {
		return Lease{}, ErrClaimScope
	}
	if input.Supersession != nil {
		if err := input.Supersession.validate(snapshot); err != nil {
			return Lease{}, err
		}
	}
	state := c.runtime(snapshot.Runtime)
	if state.closed && (input.Supersession == nil || !canSupersede(state)) {
		return Lease{}, ErrAdmissionClosed
	}
	if !state.closed && input.Supersession != nil {
		return Lease{}, ErrAdmissionClosed
	}
	if _, used := state.claims[input.ClaimID]; used {
		return Lease{}, ErrClaimReplay
	}
	previous := ""
	if len(state.entries) != 0 {
		previous = state.entries[len(state.entries)-1].Hash
	}
	if input.PreviousHash != previous {
		return Lease{}, ErrLedgerConflict
	}
	leaseID := c.nextID()
	if leaseID == "" {
		return Lease{}, fmt.Errorf("empty test lease ID")
	}
	state.closed = true
	state.generation++
	lease := Lease{
		ClaimID:      input.ClaimID,
		LeaseID:      leaseID,
		Runtime:      input.Runtime,
		Claimant:     input.Claimant,
		SnapshotHash: input.SnapshotDigest,
		Generation:   state.generation,
		IssuedAt:     input.IssuedAt,
		StartBy:      input.StartBy,
		ExpiresAt:    input.ExpiresAt,
		Supersession: cloneSupersession(input.Supersession),
	}
	entry := c.append(state, lease, StatePrepared, "")
	lease.LedgerSeq, lease.LedgerHash = entry.Sequence, entry.Hash
	state.claims[lease.ClaimID] = StatePrepared
	storedLease := lease
	storedLease.Supersession = cloneSupersession(lease.Supersession)
	state.leases[lease.LeaseID] = storedLease
	return lease, nil
}

func (c *InMemory) Start(ctx context.Context, lease Lease, snapshot Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, stored, err := c.prepared(lease)
	if err != nil {
		return err
	}
	if snapshot.Runtime != stored.Runtime || snapshot.Digest != stored.SnapshotHash {
		return ErrActiveSetChanged
	}
	now := c.now()
	if !stored.ExpiresAt.After(now) {
		return ErrClaimExpired
	}
	if !stored.StartBy.After(now) {
		return ErrDelayedStart
	}
	if !state.closed || state.generation != stored.Generation {
		return ErrAdmissionClosed
	}
	c.append(state, stored, StateStarted, "")
	state.claims[stored.ClaimID] = StateStarted
	return nil
}

func (c *InMemory) Complete(ctx context.Context, lease Lease, evidence string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.terminal(lease, StateCompleted, evidence)
}

func (c *InMemory) Fail(ctx context.Context, lease Lease, evidence string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.terminal(lease, StateFailed, evidence)
}

func (c *InMemory) Abort(ctx context.Context, lease Lease, evidence string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, stored, err := c.prepared(lease)
	if err != nil {
		return err
	}
	c.append(state, stored, StateAborted, evidence)
	state.claims[stored.ClaimID] = StateAborted
	refreshClosed(state)
	return nil
}

func (c *InMemory) terminal(lease Lease, terminal State, evidence string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.runtimes[lease.Runtime]
	if state == nil {
		return ErrUnknownLease
	}
	stored, found := state.leases[lease.LeaseID]
	if !found || !sameLease(stored, lease) {
		return ErrUnknownLease
	}
	if state.claims[stored.ClaimID] != StateStarted {
		return ErrInvalidTransition
	}
	c.append(state, stored, terminal, evidence)
	state.claims[stored.ClaimID] = terminal
	refreshClosed(state)
	return nil
}

func canSupersede(state *runtimeState) bool {
	started, prepared := 0, 0
	for _, status := range state.claims {
		switch status {
		case StateStarted:
			started++
		case StatePrepared:
			prepared++
		}
	}
	return started == 1 && prepared == 0
}

func refreshClosed(state *runtimeState) {
	state.closed = false
	for _, status := range state.claims {
		if status == StatePrepared || status == StateStarted {
			state.closed = true
			return
		}
	}
}

func cloneSupersession(value *Supersession) *Supersession {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func sameLease(left, right Lease) bool {
	return left.ClaimID == right.ClaimID && left.LeaseID == right.LeaseID && left.Runtime == right.Runtime && left.Claimant == right.Claimant && left.SnapshotHash == right.SnapshotHash && left.Generation == right.Generation && left.IssuedAt.Equal(right.IssuedAt) && left.StartBy.Equal(right.StartBy) && left.ExpiresAt.Equal(right.ExpiresAt) && left.LedgerSeq == right.LedgerSeq && left.LedgerHash == right.LedgerHash && sameSupersession(left.Supersession, right.Supersession)
}

func (c *InMemory) prepared(lease Lease) (*runtimeState, Lease, error) {
	state := c.runtimes[lease.Runtime]
	if state == nil {
		return nil, Lease{}, ErrUnknownLease
	}
	stored, found := state.leases[lease.LeaseID]
	if !found || !sameLease(stored, lease) {
		return nil, Lease{}, ErrUnknownLease
	}
	if state.claims[stored.ClaimID] != StatePrepared {
		return nil, Lease{}, ErrInvalidTransition
	}
	return state, stored, nil
}

func (c *InMemory) append(state *runtimeState, lease Lease, transition State, evidence string) LedgerEntry {
	previous := ""
	if len(state.entries) != 0 {
		previous = state.entries[len(state.entries)-1].Hash
	}
	evidenceDigest := ""
	if evidence != "" {
		sum := sha256.Sum256([]byte(evidence))
		evidenceDigest = hex.EncodeToString(sum[:])
	}
	entry := LedgerEntry{
		Sequence:       uint64(len(state.entries) + 1),
		PriorHash:      previous,
		ClaimID:        lease.ClaimID,
		LeaseID:        lease.LeaseID,
		Runtime:        lease.Runtime,
		Claimant:       lease.Claimant,
		SnapshotHash:   lease.SnapshotHash,
		State:          transition,
		EvidenceDigest: evidenceDigest,
		At:             c.now(),
	}
	entry.Hash = ledgerHash(entry)
	state.entries = append(state.entries, entry)
	return entry
}

func (c *InMemory) Status(_ context.Context, runtime string) Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.runtimes[runtime]
	if state == nil {
		return Status{Runtime: runtime}
	}
	entries := append([]LedgerEntry(nil), state.entries...)
	status := Status{Runtime: runtime, AdmissionClosed: state.closed, Generation: state.generation, Entries: entries}
	if len(entries) != 0 {
		status.Tip = entries[len(entries)-1]
		status.State = status.Tip.State
	}
	return status
}
