//go:build e2e

package daemon

import (
	"crypto/ed25519"
	"crypto/sha256"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/admission"
)

// newDefaultAdmissionCoordinator is compiled only into the isolated e2e
// binary. It supplies deterministic in-memory adversarial coverage without
// creating a production endpoint, credential, deployment, or fallback.
func newDefaultAdmissionCoordinator() admission.RuntimeCoordinator {
	seed := sha256.Sum256([]byte("no-mistakes e2e admission coordinator"))
	coordinator, err := admission.NewInMemory(admission.InMemoryConfig{
		PrivateKey: ed25519.NewKeyFromSeed(seed[:]),
		Clock: func() time.Time {
			return time.Date(2026, 7, 24, 15, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		panic(err)
	}
	return coordinator
}
