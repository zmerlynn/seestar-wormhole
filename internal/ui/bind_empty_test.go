package ui

import "testing"

func TestReviewBlankBindDoesNotWiden(t *testing.T) {
	s := testSettings()
	s.Bind = ""
	s.Discovery = false
	ports, _, err := s.validate()
	if err == nil {
		t.Log("accepted blank bind; effective listen category:", diagnosticSettingsFor(s, ports).Listen)
		t.Fatal("blank Other address accepted instead of a bind-field error")
	}
}

func TestWildcardListenProjection(t *testing.T) {
	for _, bind := range []string{"", "0.0.0.0", "::", "0:0:0:0:0:0:0:0", "::ffff:0.0.0.0"} {
		s := testSettings()
		s.Bind = bind
		s.Discovery = false
		if got := diagnosticSettingsFor(s, nil).Listen; got != "all_networks" {
			t.Errorf("wildcard categorized as %s", got)
		}
	}
}
