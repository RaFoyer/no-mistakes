package db

import (
	"database/sql"
	"fmt"
)

// AdmissionReceipt is a local correlation record for a coordinator lease.
// It is deliberately not a lease, admission lock, ledger, or recovery source:
// the independently operated coordinator remains authoritative.
type AdmissionReceipt struct {
	RunID        string
	Runtime      string
	ClaimID      string
	LeaseID      string
	Generation   uint64
	SnapshotHash string
	LedgerSeq    uint64
	LedgerHash   string
	State        string
	CreatedAt    int64
	UpdatedAt    int64
}

func (d *DB) InsertAdmissionReceipt(receipt AdmissionReceipt) error {
	if receipt.RunID == "" || receipt.Runtime == "" || receipt.ClaimID == "" || receipt.LeaseID == "" || receipt.SnapshotHash == "" || receipt.LedgerHash == "" || receipt.State != "started" || receipt.Generation == 0 || receipt.LedgerSeq == 0 {
		return fmt.Errorf("invalid admission receipt")
	}
	ts := now()
	_, err := d.sql.Exec(
		`INSERT INTO admission_receipts (run_id, runtime, claim_id, lease_id, generation, snapshot_hash, ledger_seq, ledger_hash, state, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		receipt.RunID, receipt.Runtime, receipt.ClaimID, receipt.LeaseID, receipt.Generation, receipt.SnapshotHash, receipt.LedgerSeq, receipt.LedgerHash, receipt.State, ts, ts,
	)
	if err != nil {
		return fmt.Errorf("insert admission receipt: %w", err)
	}
	return nil
}

func (d *DB) GetAdmissionReceipt(runID string) (*AdmissionReceipt, error) {
	receipt := &AdmissionReceipt{}
	err := d.sql.QueryRow(
		`SELECT run_id, runtime, claim_id, lease_id, generation, snapshot_hash, ledger_seq, ledger_hash, state, created_at, updated_at
		 FROM admission_receipts WHERE run_id = ?`, runID,
	).Scan(&receipt.RunID, &receipt.Runtime, &receipt.ClaimID, &receipt.LeaseID, &receipt.Generation, &receipt.SnapshotHash, &receipt.LedgerSeq, &receipt.LedgerHash, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get admission receipt: %w", err)
	}
	return receipt, nil
}

// ListAdmissionReceiptsForRepo is read-only correlation evidence for one
// repository. It intentionally cannot return a runtime-wide authoritative view.
func (d *DB) ListAdmissionReceiptsForRepo(repoID string) ([]AdmissionReceipt, error) {
	rows, err := d.sql.Query(
		`SELECT a.run_id, a.runtime, a.claim_id, a.lease_id, a.generation, a.snapshot_hash, a.ledger_seq, a.ledger_hash, a.state, a.created_at, a.updated_at
		 FROM admission_receipts a JOIN runs r ON r.id = a.run_id WHERE r.repo_id = ? ORDER BY a.created_at DESC, a.run_id DESC`, repoID,
	)
	if err != nil {
		return nil, fmt.Errorf("list admission receipts: %w", err)
	}
	defer rows.Close()
	var receipts []AdmissionReceipt
	for rows.Next() {
		var receipt AdmissionReceipt
		if err := rows.Scan(&receipt.RunID, &receipt.Runtime, &receipt.ClaimID, &receipt.LeaseID, &receipt.Generation, &receipt.SnapshotHash, &receipt.LedgerSeq, &receipt.LedgerHash, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan admission receipt: %w", err)
		}
		receipts = append(receipts, receipt)
	}
	return receipts, rows.Err()
}

// UpdateAdmissionReceiptTerminal marks only a terminal external transition.
// This update is observability evidence after the coordinator accepted its
// terminal ledger entry; it never reopens local admission by itself.
func (d *DB) UpdateAdmissionReceiptTerminal(runID, state string) error {
	switch state {
	case "completed", "failed", "aborted":
	default:
		return fmt.Errorf("admission receipt state %q is not terminal", state)
	}
	result, err := d.sql.Exec(`UPDATE admission_receipts SET state = ?, updated_at = ? WHERE run_id = ?`, state, now(), runID)
	if err != nil {
		return fmt.Errorf("update admission receipt terminal: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("admission receipt rows affected: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("admission receipt not found for run %s", runID)
	}
	return nil
}
