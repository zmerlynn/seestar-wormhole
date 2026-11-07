//go:build gui

package ui

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestSandboxPreflight(t *testing.T) {
	for _, failure := range []error{nil, errors.New("uid map denied"), errors.New("executable missing"), context.DeadlineExceeded} {
		called := false
		err := probeWindowSandbox(t.Context(), func(_ context.Context, name string, args ...string) error {
			called = true
			want := []string{"--ro-bind", "/", "/", "--unshare-user", "--unshare-pid", "--proc", "/proc", "--dev", "/dev", "--", "/bin/true"}
			if name != "bwrap" || !reflect.DeepEqual(args, want) {
				t.Fatalf("unexpected sandbox probe: %s %q", name, args)
			}
			return failure
		})
		if !called || (err == nil) != (failure == nil) {
			t.Fatalf("failure %v returned %v", failure, err)
		}
		// The archive has no docs, so the message links the published page.
		if err != nil && err.Error() != "WebKit sandbox unavailable. Install bubblewrap and allow unprivileged user namespaces. See https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/troubleshooting.md#linux-native-window" {
			t.Fatalf("unclear startup error: %v", err)
		}
	}
}
