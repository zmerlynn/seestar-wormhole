package ui

import (
	"testing"
)

func TestOSResolvedBindLoads(t *testing.T) {
	st := testStorage(t)
	// OS-resolved binds remain available for saved settings used by the CLI.
	if err := st.writeBytes("config.json", []byte(`{"version":1,"bind":"localhost","idle":"0"}`)); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(st)
	if err != nil {
		t.Fatal("valid OS-resolved bind rejected")
	}
	if s.Bind != "localhost" || s.Discovery {
		t.Fatal("saved exposure changed")
	}
}
