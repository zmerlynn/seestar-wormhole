package ui

import (
	"testing"
)

func TestLegacyOSResolvedBindLoads(t *testing.T) {
	st := testStorage(t)
	// Literal schema-v1 fixture: the old UI and TCP CLI allowed OS-resolved binds.
	if err := st.writeBytes("config.json", []byte(`{"version":1,"bind":"localhost","idle":"0"}`)); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(st)
	if err != nil {
		t.Fatal("valid legacy OS-resolved bind rejected during upgrade")
	}
	if s.Bind != "localhost" || s.Discovery {
		t.Fatal("legacy exposure changed")
	}
}
