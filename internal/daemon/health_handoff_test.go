package daemon

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
)

func TestHealthExposesNonSecretHandoffCapabilities(t *testing.T) {
	p, _ := startTestDaemon(t)
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var health ipc.HealthResult
	if err := client.Call(ipc.MethodHealth, &ipc.HealthParams{}, &health); err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.Generation == "" || health.Build == "" {
		t.Fatalf("health identity = %+v", health)
	}
	if health.HandoffProtocol != CurrentHandoffProtocol || health.SchemaVersion != CurrentHandoffSchema {
		t.Fatalf("health compatibility = %+v", health)
	}
	if health.MaintenancePhase == "" {
		t.Fatalf("health omitted maintenance phase: %+v", health)
	}
}
