package daemon

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestPushReceivedWritesOnlyCorrelationReceiptAndTerminalState(t *testing.T) {
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&mockPassStep{name: types.StepReview}}
	})
	_, headSHA := setupTestGitRepo(t, p, d, "admission-receipt-repo")
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir("admission-receipt-repo"), Ref: "refs/heads/main",
		Old: "0000000000000000000000000000000000000000", New: headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}
	if run := waitForRunTerminalState(t, d, result.RunID); run.Status != types.RunCompleted {
		t.Fatalf("run status = %s", run.Status)
	}
	receipt, err := d.GetAdmissionReceipt(result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt == nil || receipt.State != "completed" || receipt.ClaimID == "" || receipt.LeaseID == "" || receipt.LedgerHash == "" {
		t.Fatalf("receipt = %#v", receipt)
	}
}
