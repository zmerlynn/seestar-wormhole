//go:build !windows

package cloud

// Unix errno leaves already match the original switch. Do not invoke custom Is
// methods at this default branch: unrecognized Unix leaves must stay fatal.
func nativeRefusal(error) bool { return false }
