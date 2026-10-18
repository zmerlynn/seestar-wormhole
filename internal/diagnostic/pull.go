package diagnostic

import "errors"

// PullReason identifies a pull safety refusal without carrying archive names.
type PullReason uint8

const (
	// FutureMtime means the source timestamp is ahead of the run clock.
	FutureMtime PullReason = iota
	// InvalidMetadata means the source has no usable timestamp or size.
	InvalidMetadata
	// CaseCollision means two archive paths collide under portable comparison.
	CaseCollision
	// InventoryLimit means a local or remote inventory exceeded its entry budget.
	InventoryLimit
	// MetadataLimit means the destination state exceeded its byte budget.
	MetadataLimit
	// VerifyMismatch means a copy failed its length/byte-count or remote comparison check.
	VerifyMismatch
	// RemoteChanged means source metadata changed during a transfer.
	RemoteChanged
	// DiskFull means a destination write or preallocation exhausted free space.
	DiskFull
	// LockHeld means another process owns the destination lock.
	LockHeld
	// UnsafePath means a source or destination path violates archive safety rules.
	UnsafePath
	// StateInvalid means destination state is corrupt or unsupported.
	StateInvalid
	// SourceMismatch means the archive belongs to a different source.
	SourceMismatch
	// OperationPanic means a contained pull worker or backend operation panicked.
	OperationPanic
	// MissingDirectory means a requested share directory does not exist.
	MissingDirectory
	// LayoutMismatch means the destination uses the other pull layout.
	LayoutMismatch
	// MetadataPermissions means private destination metadata grants group/other access.
	MetadataPermissions
)

var pullReasons = [...]struct{ code, action string }{
	{"future_mtime", "check the device and host clocks, then retry"},
	{"invalid_metadata", "check source timestamps and file sizes, then retry"},
	{"case_collision", "resolve case or Unicode name collisions in the source and archive"},
	{"inventory_limit", "reduce the archive inventory below 200000 entries"},
	{"metadata_limit", "restore or move destination state below 64 MiB"},
	{"verify_mismatch", "check source stability and storage, then retry; prior archive bytes are retained"},
	{"remote_changed", "wait for source writes to finish, then retry"},
	{"disk_full", "free space on the destination filesystem, then retry"},
	{"lock_held", "wait for the other pull process to exit, then retry"},
	{"unsafe_path", "resolve unsafe names, symlinks or nonregular paths"},
	{"state_invalid", "restore or move corrupt/unsupported destination state explicitly"},
	{"source_mismatch", "use the original source, choose another destination, or use --rebind to take over"},
	{"operation_panic", "report the pull failure, then retry after checking session release"},
	{"missing_directory", "check that every --dir exists under EMMC Images on the device"},
	{"layout_mismatch", "use another destination; explicit --dir and default MyWorks layouts cannot share state, even with --rebind"},
	{"metadata_permissions", "set the metadata directory (.seestar-wormhole) to 0700 and metadata files to 0600; share delivered files without widening the metadata directory"},
}

// ErrPullPanic is the sanitized typed outcome of a contained pull panic.
var ErrPullPanic error = &PullError{Reason: OperationPanic}

// PullError carries a fixed diagnostic and an optional inspectable cause. Its
// Error method never formats the cause, which may contain private backend paths.
type PullError struct {
	Reason PullReason
	Cause  error
}

func (e *PullError) Error() string { return pullClass(e.Reason) }
func (e *PullError) Unwrap() error { return e.Cause }

func pullClass(reason PullReason) string {
	if int(reason) >= len(pullReasons) {
		return "operation_failed"
	}
	return pullReasons[reason].code
}

// PullAction returns a fixed recovery hint for a classified pull outcome.
func PullAction(reason string) string {
	for _, detail := range pullReasons {
		if reason == detail.code {
			return detail.action
		}
	}
	switch reason {
	case "cloud_license_missing":
		return licenseMissingHint
	case "busy":
		return "wait for the device session owner to release it, or use --busy-wait"
	case "timeout":
		return "check connectivity or increase --timeout, then retry"
	case "canceled":
		return "rerun to retain completed receipts and restart incomplete files"
	case "invalid_option", "invalid_configuration":
		return "check pull options and environment; see seestar-wormhole pull --help"
	case "release_failed", "ownership_uncertain":
		return "check session release before starting another pull"
	}
	return "check connectivity, credentials and destination access, then retry"
}

func typedPullClass(err error) string {
	var failure *PullError
	if errors.As(err, &failure) {
		return pullClass(failure.Reason)
	}
	return ""
}
