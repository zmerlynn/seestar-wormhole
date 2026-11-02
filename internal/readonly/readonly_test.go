package readonly

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	t.Cleanup(Reset)
	Check("before") // off by default
	Enable()
	defer func() {
		r := recover()
		if s, ok := r.(string); !ok || !strings.Contains(s, "state.Append would change the local filesystem during a dry run") {
			t.Errorf("Check after Enable panicked with %v", r)
		}
		Reset()
		Check("after Reset")
	}()
	Check("state.Append")
	t.Error("Check after Enable returned")
}
