package sessionedge

import "testing"

// These seed prefixes retain the runtime orderings independently of the pure
// simulator: cancellation races a busy result or cleanup, and an allocator can
// fail its internal stop without handing back a lease.
func TestSeededRuntimeFailureCases(t *testing.T) {
	for _, seed := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"4/late-busy", TestLateBusyDoesNotRetry},
		{"5/canceled-stop", TestCancellationReportsStopFailure},
		{"6/internal-stop", TestAllocatorStopFailureRecoversAfterDelay},
	} {
		t.Run(seed.name, seed.run)
	}
}
