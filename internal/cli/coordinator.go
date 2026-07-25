package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/spf13/cobra"
)

// newCoordinatorCmd exposes only read-only evidence while the external
// coordinator adapter remains deliberately inactive. It never dials, starts,
// restarts, configures, or otherwise mutates a runtime.
func newCoordinatorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "coordinator",
		Short: "Inspect shared-runtime admission evidence",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show read-only admission correlation evidence",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return trackReadSurface("coordinator-status", nil, func() (string, string, error) {
				_, database, err := openResources()
				if err != nil {
					return "", "", err
				}
				defer database.Close()
				repo, err := findRepo(database)
				if err != nil {
					return "", "", err
				}
				fingerprint, err := renderCoordinatorStatus(cmd.OutOrStdout(), database, repo.ID)
				return fingerprint, "", err
			})
		},
	})
	return cmd
}

func renderCoordinatorStatus(w io.Writer, database *db.DB, repoID string) (string, error) {
	receipts, err := database.ListAdmissionReceiptsForRepo(repoID)
	if err != nil {
		return "", err
	}
	// This source lane intentionally ships no endpoint or credentials. The
	// fixed message makes it explicit that these local rows are correlation
	// evidence, never an authority capable of reopening admission or recovery.
	fmt.Fprintln(w, "external coordinator: inactive (no adapter installed)")
	fmt.Fprintln(w, "local correlation evidence (not a ledger, lease, or recovery authority):")
	if len(receipts) == 0 {
		fmt.Fprintln(w, "  none")
		return repoID + "|inactive|none", nil
	}
	var fingerprint strings.Builder
	fingerprint.WriteString(repoID)
	fingerprint.WriteString("|inactive")
	for _, receipt := range receipts {
		fmt.Fprintf(w, "  run=%s claim=%s lease=%s state=%s generation=%d\n", receipt.RunID, receipt.ClaimID, receipt.LeaseID, receipt.State, receipt.Generation)
		fmt.Fprintf(&fingerprint, "|%s:%s:%s:%s:%d:%s", receipt.RunID, receipt.ClaimID, receipt.LeaseID, receipt.State, receipt.Generation, receipt.LedgerHash)
	}
	return fingerprint.String(), nil
}
