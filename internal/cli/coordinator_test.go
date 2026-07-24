package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
)

func TestRenderCoordinatorStatusIsReadOnlyAndLabelsLocalEvidence(t *testing.T) {
	database := openTestDB(t)
	repo, err := database.InsertRepo(t.TempDir(), "origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.InsertRun(repo.ID, "feature/admission", "head", "base")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.InsertAdmissionReceipt(db.AdmissionReceipt{
		RunID: run.ID, Runtime: "no-mistakes/runtime/test", ClaimID: "claim-1", LeaseID: "lease-1",
		Generation: 1, SnapshotHash: "snapshot", LedgerSeq: 1, LedgerHash: "ledger", State: "started",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAdmissionReceiptTerminal(run.ID, "completed"); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	fingerprint, err := renderCoordinatorStatus(&out, database, repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"external coordinator: inactive", "local correlation evidence", "claim-1", "completed"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(fingerprint, "claim-1") || !strings.Contains(fingerprint, "completed") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
}
