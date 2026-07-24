//go:build !e2e

package daemon

import "github.com/kunchenguid/no-mistakes/internal/admission"

// newDefaultAdmissionCoordinator is deliberately inactive in every normal
// binary. Installing an independently operated adapter is a later governed
// delivery lane; local daemon state is never a fallback authority.
func newDefaultAdmissionCoordinator() admission.RuntimeCoordinator {
	return admission.NewInactive()
}
