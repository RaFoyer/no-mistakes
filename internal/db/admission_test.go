package db

import "testing"

func TestAdmissionReceiptRoundTripAndTerminalUpdate(t *testing.T) {
	d := openTestDB(t)
	repo, err := d.InsertRepo(t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := d.InsertRun(repo.ID, "feature/admission", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	want := AdmissionReceipt{
		RunID:        run.ID,
		Runtime:      "no-mistakes/runtime/test",
		ClaimID:      "claim-1",
		LeaseID:      "lease-1",
		Generation:   3,
		SnapshotHash: "snapshot",
		LedgerSeq:    7,
		LedgerHash:   "ledger",
		State:        "started",
	}
	if err := d.InsertAdmissionReceipt(want); err != nil {
		t.Fatalf("insert receipt: %v", err)
	}
	got, err := d.GetAdmissionReceipt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Runtime != want.Runtime || got.ClaimID != want.ClaimID || got.State != "started" || got.Generation != 3 {
		t.Fatalf("receipt = %#v", got)
	}
	if err := d.UpdateAdmissionReceiptTerminal(run.ID, "completed"); err != nil {
		t.Fatalf("terminal update: %v", err)
	}
	got, err = d.GetAdmissionReceipt(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "completed" || got.UpdatedAt < got.CreatedAt {
		t.Fatalf("terminal receipt = %#v", got)
	}
}

func TestAdmissionReceiptRejectsNonterminalOrMismatchedState(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpdateAdmissionReceiptTerminal("missing", "prepared"); err == nil {
		t.Fatal("accepted nonterminal state")
	}
}
