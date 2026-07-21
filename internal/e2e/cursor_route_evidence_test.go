//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// TestCursorRouteEvidenceSurface verifies the reviewer-visible route-evidence
// command after a real Cursor-backed pipeline run. It complements adapter unit
// tests by exercising daemon provisioning, the native stream-json process, and
// persisted route evidence through the public CLI.
func TestCursorRouteEvidenceSurface(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "cursor", Scenario: cleanReviewScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}
	h.CommitChange("cursor-route-evidence", "cursor.txt", "native cursor route\n", "exercise cursor route evidence")
	h.PushToGate("cursor-route-evidence")
	run := h.WaitForRun("cursor-route-evidence", 60*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, error=%v", run.Status, deref(run.Error))
	}

	out, err := h.Run("axi", "route-evidence", "--run", run.ID)
	if err != nil {
		t.Fatalf("axi route-evidence: %v\n%s", err, out)
	}
	for _, want := range []string{"requested_harness", "effective_harness", "cursor", "cursor-grok-4.5-medium", "stdin"} {
		if !strings.Contains(out, want) {
			t.Errorf("route evidence missing %q:\n%s", want, out)
		}
	}
	t.Logf("reviewer-visible `no-mistakes axi route-evidence --run %s` output:\n%s", run.ID, out)
}
