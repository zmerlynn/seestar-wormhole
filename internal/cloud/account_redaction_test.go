package cloud

import (
	"strings"
	"testing"
)

func TestAccountErrorRedactsCode(t *testing.T) {
	_, err := ParseLogin(400, []byte(`{"code":87654321}`), false)
	if strings.Contains(err.Error(), "87654321") {
		t.Fatal("response value escaped account error")
	}
}
