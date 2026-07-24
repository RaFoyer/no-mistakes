package admission

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"
)

var testClaimant = Claimant{Kind: "firstmate-manager", ID: "q9"}

func testSigningKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("no-mistakes shared-runtime admission test signer"))
	return ed25519.NewKeyFromSeed(seed[:])
}

func testSnapshot() Snapshot {
	return NewSnapshot("nm-runtime/test", []ActiveRun{{
		ID: "run-a", RepoID: "repo-a", Branch: "feature/a", HeadSHA: "aaaaaaaa", Status: "running",
	}})
}

func deterministicIDs() func() string {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("test-id-%d", next)
	}
}

func TestSnapshotIsCanonicalExactActiveSet(t *testing.T) {
	runs := []ActiveRun{
		{ID: "run-b", RepoID: "repo", Branch: "b", HeadSHA: "b", Status: "running"},
		{ID: "run-a", RepoID: "repo", Branch: "a", HeadSHA: "a", Status: "pending"},
	}
	first := NewSnapshot("nm-runtime/test", runs)
	second := NewSnapshot("nm-runtime/test", []ActiveRun{runs[1], runs[0]})
	if first.Digest != second.Digest {
		t.Fatalf("canonical sets differ: %s != %s", first.Digest, second.Digest)
	}
	first.Runs[0].HeadSHA = "tampered"
	if err := first.Validate(); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("tampered snapshot error = %v, want %v", err, ErrInvalidSnapshot)
	}
}

func TestClaimAuthenticationRejectsForgeryFutureAndExpiry(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	key := testSigningKey()
	snapshot := testSnapshot()
	input := ClaimInput{ClaimID: "claim-a", Runtime: snapshot.Runtime, Claimant: testClaimant, SnapshotDigest: snapshot.Digest, IssuedAt: now, StartBy: now.Add(time.Second), ExpiresAt: now.Add(time.Minute)}
	claim, err := SignClaim(key, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyClaim(claim, key.Public().(ed25519.PublicKey), input.Runtime, input.Claimant, now); err != nil {
		t.Fatalf("verify valid claim: %v", err)
	}
	supersession := &Supersession{Target: snapshot.Runs[0]}
	supersedingInput := input
	supersedingInput.ClaimID = "claim-superseding"
	supersedingInput.Supersession = supersession
	supersedingClaim, err := SignClaim(key, supersedingInput)
	if err != nil {
		t.Fatal(err)
	}
	tamperedSupersedingClaim := supersedingClaim
	tamperedSupersedingClaim.Input.Supersession = &Supersession{Target: ActiveRun{ID: "run-a", RepoID: "repo-a", Branch: "feature/other", HeadSHA: "aaaaaaaa", Status: "running"}}
	if err := VerifyClaim(tamperedSupersedingClaim, key.Public().(ed25519.PublicKey), input.Runtime, input.Claimant, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered supersession error = %v, want %v", err, ErrInvalidSignature)
	}
	forged := claim
	forged.Signature = append([]byte(nil), claim.Signature...)
	forged.Signature[0] ^= 0xff
	if err := VerifyClaim(forged, key.Public().(ed25519.PublicKey), input.Runtime, input.Claimant, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("forged claim error = %v, want %v", err, ErrInvalidSignature)
	}
	futureInput := input
	futureInput.ClaimID = "claim-future"
	futureInput.IssuedAt = now.Add(time.Second)
	futureInput.StartBy = now.Add(2 * time.Second)
	futureInput.ExpiresAt = now.Add(time.Minute)
	future, err := SignClaim(key, futureInput)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyClaim(future, key.Public().(ed25519.PublicKey), input.Runtime, input.Claimant, now); !errors.Is(err, ErrClaimFuture) {
		t.Fatalf("future claim error = %v, want %v", err, ErrClaimFuture)
	}
	if err := VerifyClaim(claim, key.Public().(ed25519.PublicKey), input.Runtime, input.Claimant, now.Add(time.Minute)); !errors.Is(err, ErrClaimExpired) {
		t.Fatalf("expired claim error = %v, want %v", err, ErrClaimExpired)
	}
	if err := VerifyClaim(claim, key.Public().(ed25519.PublicKey), input.Runtime, Claimant{Kind: testClaimant.Kind, ID: "other"}, now); !errors.Is(err, ErrClaimScope) {
		t.Fatalf("foreign claimant error = %v, want %v", err, ErrClaimScope)
	}
}

func TestInMemoryCoordinatorLifecycleCASReplayAndReopen(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	key := testSigningKey()
	coordinator, err := NewInMemory(InMemoryConfig{PrivateKey: key, Clock: func() time.Time { return now }, NextID: deterministicIDs()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	req := Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot}
	claim, err := coordinator.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !lease.Immutable() {
		t.Fatalf("lease lacks immutable bounds: %+v", lease)
	}
	if lease.Claimant != req.Claimant {
		t.Fatalf("lease claimant = %+v, want %+v", lease.Claimant, req.Claimant)
	}
	if _, err := coordinator.Acquire(ctx, req); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("concurrent acquire error = %v, want %v", err, ErrAdmissionClosed)
	}
	changed := NewSnapshot(snapshot.Runtime, append(snapshot.Runs, ActiveRun{ID: "run-b", RepoID: "repo-b", Branch: "feature/b", HeadSHA: "bbbbbbbb", Status: "running"}))
	if err := coordinator.Start(ctx, lease, changed); !errors.Is(err, ErrActiveSetChanged) {
		t.Fatalf("changed-set start error = %v, want %v", err, ErrActiveSetChanged)
	}
	if err := coordinator.Abort(ctx, lease, "active set changed"); err != nil {
		t.Fatal(err)
	}
	status := coordinator.Status(ctx, snapshot.Runtime)
	if status.AdmissionClosed || status.State != StateAborted || len(status.Entries) != 2 {
		t.Fatalf("aborted status = %+v", status)
	}
	if err := ValidateLedger(status.Entries); err != nil {
		t.Fatalf("aborted ledger: %v", err)
	}
	if _, err := coordinator.Prepare(ctx, claim, snapshot); !errors.Is(err, ErrClaimReplay) {
		t.Fatalf("replayed claim error = %v, want %v", err, ErrClaimReplay)
	}

	claim, err = coordinator.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, lease, "bounded terminal evidence"); err != nil {
		t.Fatal(err)
	}
	status = coordinator.Status(ctx, snapshot.Runtime)
	if status.AdmissionClosed || status.State != StateCompleted || len(status.Entries) != 5 {
		t.Fatalf("completed status = %+v", status)
	}
	if err := ValidateLedger(status.Entries); err != nil {
		t.Fatalf("completed ledger: %v", err)
	}

	claim, err = coordinator.Acquire(ctx, req)
	if err != nil {
		t.Fatalf("acquire after completed terminal: %v", err)
	}
	lease, err = coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Fail(ctx, lease, "bounded failure evidence"); err != nil {
		t.Fatal(err)
	}
	status = coordinator.Status(ctx, snapshot.Runtime)
	if status.AdmissionClosed || status.State != StateFailed || len(status.Entries) != 8 {
		t.Fatalf("failed status = %+v", status)
	}
	if _, err := coordinator.Acquire(ctx, req); err != nil {
		t.Fatalf("admission did not reopen after failed terminal: %v", err)
	}
}

func TestInMemoryCoordinatorSupersessionIsScopedAndKeepsAdmissionClosed(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	empty := NewSnapshot("nm-runtime/test", nil)
	active := testSnapshot()
	request := Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: empty}
	claim, err := coordinator.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := coordinator.Prepare(ctx, claim, empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, oldLease, empty); err != nil {
		t.Fatal(err)
	}

	supersession := &Supersession{Target: active.Runs[0]}
	for _, test := range []struct {
		name   string
		target ActiveRun
	}{
		{name: "different repository", target: ActiveRun{ID: "run-a", RepoID: "repo-other", Branch: "feature/a", HeadSHA: "aaaaaaaa", Status: "running"}},
		{name: "different branch", target: ActiveRun{ID: "run-a", RepoID: "repo-a", Branch: "feature/other", HeadSHA: "aaaaaaaa", Status: "running"}},
		{name: "stale head", target: ActiveRun{ID: "run-a", RepoID: "repo-a", Branch: "feature/a", HeadSHA: "stale", Status: "running"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := *supersession
			bad.Target = test.target
			_, err := coordinator.Acquire(ctx, Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: active, Supersession: &bad})
			if !errors.Is(err, ErrSupersessionScope) {
				t.Fatalf("supersession error = %v, want %v", err, ErrSupersessionScope)
			}
		})
	}

	changed := NewSnapshot(active.Runtime, append(active.Runs, ActiveRun{ID: "run-b", RepoID: "repo-b", Branch: "feature/b", HeadSHA: "bbbbbbbb", Status: "running"}))
	claim, err = coordinator.Acquire(ctx, Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: active, Supersession: supersession})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := coordinator.Prepare(ctx, claim, active)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, prepared, changed); !errors.Is(err, ErrActiveSetChanged) {
		t.Fatalf("changed active set error = %v, want %v", err, ErrActiveSetChanged)
	}
	if err := coordinator.Abort(ctx, prepared, "active set changed"); err != nil {
		t.Fatal(err)
	}

	claim, err = coordinator.Acquire(ctx, Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: active, Supersession: supersession})
	if err != nil {
		t.Fatal(err)
	}
	newLease, err := coordinator.Prepare(ctx, claim, active)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, newLease, active); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Acquire(ctx, Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: active}); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatalf("unrelated start error = %v, want %v", err, ErrAdmissionClosed)
	}
	status := coordinator.Status(ctx, active.Runtime)
	if !status.AdmissionClosed {
		t.Fatal("admission reopened while superseded and superseding leases were active")
	}
	if err := coordinator.Complete(ctx, oldLease, "superseded"); err != nil {
		t.Fatal(err)
	}
	if status := coordinator.Status(ctx, active.Runtime); !status.AdmissionClosed {
		t.Fatal("admission reopened after only the superseded lease terminalized")
	}
	if err := coordinator.Complete(ctx, newLease, "completed"); err != nil {
		t.Fatal(err)
	}
	status = coordinator.Status(ctx, active.Runtime)
	if status.AdmissionClosed || status.State != StateCompleted {
		t.Fatalf("terminal supersession status = %+v", status)
	}
	if err := ValidateLedger(status.Entries); err != nil {
		t.Fatalf("supersession ledger: %v", err)
	}
}

func TestInMemoryCoordinatorSupersessionRaceHasOneWinner(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	empty := NewSnapshot("nm-runtime/test", nil)
	active := testSnapshot()
	claim, err := coordinator.Acquire(ctx, Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: empty})
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := coordinator.Prepare(ctx, claim, empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, oldLease, empty); err != nil {
		t.Fatal(err)
	}

	request := Request{Runtime: active.Runtime, Claimant: testClaimant, Snapshot: active, Supersession: &Supersession{Target: active.Runs[0]}}
	const contenders = 16
	start := make(chan struct{})
	leases := make(chan Lease, contenders)
	errs := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		go func() {
			<-start
			claim, err := coordinator.Acquire(ctx, request)
			if err == nil {
				var lease Lease
				lease, err = coordinator.Prepare(ctx, claim, active)
				if err == nil {
					err = coordinator.Start(ctx, lease, active)
					if err == nil {
						leases <- lease
					}
				}
			}
			errs <- err
		}()
	}
	close(start)
	var winner Lease
	winners := 0
	for i := 0; i < contenders; i++ {
		if err := <-errs; err == nil {
			winners++
			winner = <-leases
		} else if !errors.Is(err, ErrAdmissionClosed) && !errors.Is(err, ErrLedgerConflict) {
			t.Fatalf("supersession race error = %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("supersession race winners = %d, want 1", winners)
	}
	status := coordinator.Status(ctx, active.Runtime)
	if !status.AdmissionClosed || len(status.Entries) != 4 {
		t.Fatalf("supersession race status = %+v", status)
	}
	if err := coordinator.Complete(ctx, oldLease, "superseded"); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, winner, "completed"); err != nil {
		t.Fatal(err)
	}
}

func TestInMemoryCoordinatorRejectsDelayedStartAndInactiveFailsClosed(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	key := testSigningKey()
	coordinator, err := NewInMemory(InMemoryConfig{PrivateKey: key, Clock: func() time.Time { return now }, StartWindow: time.Second, NextID: deterministicIDs()})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	req := Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot}
	claim, err := coordinator.Acquire(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// StartBy is an exclusive deadline: accepting the exact boundary would
	// broaden a bounded lease by one scheduler tick.
	now = now.Add(time.Second)
	if err := coordinator.Start(ctx, lease, snapshot); !errors.Is(err, ErrDelayedStart) {
		t.Fatalf("delayed start error = %v, want %v", err, ErrDelayedStart)
	}
	if err := coordinator.Abort(ctx, lease, "delayed start"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewInactive().Acquire(ctx, req); !errors.Is(err, ErrCoordinatorUnavailable) {
		t.Fatalf("inactive acquire error = %v, want %v", err, ErrCoordinatorUnavailable)
	}
}

func TestLeaseValidateForBindsClaimAndRequest(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Runtime: testSnapshot().Runtime, Claimant: testClaimant, Snapshot: testSnapshot()}
	claim, err := coordinator.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(context.Background(), claim, request.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.ValidateFor(claim, request); err != nil {
		t.Fatalf("validate lease: %v", err)
	}

	tests := []struct {
		name  string
		claim Claim
		lease Lease
	}{
		{name: "missing generation", claim: claim, lease: func() Lease { l := lease; l.Generation = 0; return l }()},
		{name: "different claim", claim: claim, lease: func() Lease { l := lease; l.ClaimID = "other-claim"; return l }()},
		{name: "different runtime", claim: claim, lease: func() Lease { l := lease; l.Runtime = "other-runtime"; return l }()},
		{name: "different snapshot", claim: claim, lease: func() Lease { l := lease; l.SnapshotHash = "other-snapshot"; return l }()},
		{name: "extended lease", claim: claim, lease: func() Lease { l := lease; l.ExpiresAt = l.ExpiresAt.Add(time.Minute); return l }()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.lease.ValidateFor(test.claim, request); !errors.Is(err, ErrInvalidLease) {
				t.Fatalf("validate malformed lease error = %v, want %v", err, ErrInvalidLease)
			}
		})
	}
}

func TestInMemoryCoordinatorRejectsStaleClaimAfterLedgerAdvances(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	request := Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot}

	// Both claims bind to the same empty-ledger predecessor. Only one can
	// survive once the first lease has added its terminal hash-linked record.
	fresh, err := coordinator.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := coordinator.Acquire(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, fresh, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, lease, "completed"); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Acquire(ctx, request); err != nil {
		t.Fatalf("terminal transition did not reopen admission: %v", err)
	}
	if _, err := coordinator.Prepare(ctx, stale, snapshot); !errors.Is(err, ErrLedgerConflict) {
		t.Fatalf("stale claim error = %v, want %v", err, ErrLedgerConflict)
	}
}

func TestInMemoryCoordinatorHonorsCancelledRequestsBeforeMutation(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := testSnapshot()
	request := Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.Acquire(cancelled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire error = %v, want context.Canceled", err)
	}
	if status := coordinator.Status(context.Background(), snapshot.Runtime); len(status.Entries) != 0 || status.AdmissionClosed {
		t.Fatalf("cancelled acquire changed state: %+v", status)
	}
	claim, err := coordinator.Acquire(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Prepare(cancelled, claim, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled prepare error = %v, want context.Canceled", err)
	}
	if status := coordinator.Status(context.Background(), snapshot.Runtime); len(status.Entries) != 0 || status.AdmissionClosed {
		t.Fatalf("cancelled prepare changed state: %+v", status)
	}
}

func TestLedgerRejectsTransitionTamperingEvenWhenRehashed(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	claim, err := coordinator.Acquire(ctx, Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, lease, "done"); err != nil {
		t.Fatal(err)
	}
	entries := append([]LedgerEntry(nil), coordinator.Status(ctx, snapshot.Runtime).Entries...)
	entries[1].State = StateCompleted // prepared -> completed skips started.
	entries[1].Hash = ledgerHash(entries[1])
	entries[2].PriorHash = entries[1].Hash
	entries[2].Hash = ledgerHash(entries[2])
	if err := ValidateLedger(entries); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("tampered transition error = %v, want %v", err, ErrInvalidTransition)
	}
}

func TestLedgerRejectsImmutableLeaseMutationEvenWhenRehashed(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	claim, err := coordinator.Acquire(ctx, Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, lease, "done"); err != nil {
		t.Fatal(err)
	}
	entries := append([]LedgerEntry(nil), coordinator.Status(ctx, snapshot.Runtime).Entries...)
	entries[1].SnapshotHash = "substituted-active-set"
	entries[1].Hash = ledgerHash(entries[1])
	entries[2].PriorHash = entries[1].Hash
	entries[2].Hash = ledgerHash(entries[2])
	if err := ValidateLedger(entries); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mutated lease error = %v, want %v", err, ErrInvalidTransition)
	}
}

func TestLedgerRejectsRehashedClaimReplayAcrossLeases(t *testing.T) {
	now := time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
	coordinator, err := NewInMemory(InMemoryConfig{
		PrivateKey: testSigningKey(), Clock: func() time.Time { return now }, NextID: deterministicIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	snapshot := testSnapshot()
	claim, err := coordinator.Acquire(ctx, Request{Runtime: snapshot.Runtime, Claimant: testClaimant, Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := coordinator.Prepare(ctx, claim, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Start(ctx, lease, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Complete(ctx, lease, "done"); err != nil {
		t.Fatal(err)
	}

	entries := append([]LedgerEntry(nil), coordinator.Status(ctx, snapshot.Runtime).Entries...)
	for _, entry := range append([]LedgerEntry(nil), entries...) {
		entry.Sequence = uint64(len(entries) + 1)
		entry.PriorHash = entries[len(entries)-1].Hash
		entry.LeaseID = "replayed-lease"
		entry.Hash = ledgerHash(entry)
		entries = append(entries, entry)
	}
	if err := ValidateLedger(entries); !errors.Is(err, ErrClaimReplay) {
		t.Fatalf("replayed claim error = %v, want %v", err, ErrClaimReplay)
	}
}
