package ui

import (
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxyrun"
	"testing"
)

func TestFirstRunPhoneAndSavedExposure(t *testing.T) {
	s := defaultSettings()
	ports, _, err := s.validate()
	if err != nil || !s.Discovery || s.Bind != "0.0.0.0" {
		t.Fatalf("first run: %+v %v", s, err)
	}
	found := false
	for _, p := range ports {
		if p == (proxyrun.Port{Local: 80, Device: 80}) {
			found = true
		}
	}
	if !found {
		t.Fatal("phone HTTP mapping missing")
	}
	st := testStorage(t)
	if err := st.writeBytes("config.json", []byte(`{"version":1,"bind":"127.0.0.1","idle":"0"}`)); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(st)
	if err != nil || loaded.Discovery || loaded.Bind != "127.0.0.1" || len(loaded.Ports) != 0 {
		t.Fatalf("upgrade widened settings: %+v %v", loaded, err)
	}
}
func TestFieldErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		code, field string
		change      func(*Settings)
	}{
		{"discovery_bind", "bind", func(s *Settings) { s.Bind = "127.0.0.1" }},
		{"discovery_bind", "bind", func(s *Settings) { s.Bind = "invalid" }},
		{"idle", "idle", func(s *Settings) { s.Idle = "-1m" }},
		{"email", "email", func(s *Settings) { s.Email = "invalid" }},
		{"ports", "ports", func(s *Settings) { s.Ports = []proxyrun.Port{{Local: 4700, Device: 80}} }},
	} {
		t.Run(tc.code, func(t *testing.T) {
			s := testSettings()
			tc.change(&s)
			_, _, err := s.validate()
			if err == nil {
				t.Fatal("accepted invalid settings")
			}
			e := actionError(err)
			if e.Code != tc.code || e.Field != tc.field {
				t.Fatalf("%+v", e)
			}
		})
	}
}

func TestEmailValidationUsesVendorRule(t *testing.T) {
	for _, email := range []string{"mixed.Case+tag@example.invalid", "name@domain.1"} {
		s := testSettings()
		s.Email = email
		if _, _, err := s.validate(); err != nil {
			t.Fatal(err)
		}
	}
	_, err := cloud.EmailCredentials("bad-address", "validation-only")
	if e := actionError(err); e.Code != "email" || e.Field != "email" {
		t.Fatalf("email error: %+v", e)
	}
}

func TestHTTPAccountInputPreparation(t *testing.T) {
	s := testServer(t)
	body := `{"settings":{"version":1,"bind":"127.0.0.1","idle":"0","email":"  Mixed. Case+Tag @Example.Invalid  "},"password":"synthetic-password"}`
	if w := doPost(s, "/api/password/save", body); w.Code != 200 {
		t.Fatalf("save: %s", w.Body)
	}
	saved, err := loadSettings(s.storage)
	if err != nil || saved.Email != "Mixed.Case+Tag@Example.Invalid" {
		t.Fatalf("normalization: %q %v", saved.Email, err)
	}
}

func TestCredentialValidationTargetsItsField(t *testing.T) {
	for _, tc := range []struct{ email, password, field string }{
		{"invalid", testPassword, "email"},
		{testSettings().Email, "short", "password"},
	} {
		c := &credentialStore{chain: &fakeKeychain{}}
		s := testSettings()
		s.Email = tc.email
		_, err := c.credentials(t.Context(), s, tc.password)
		if e := actionError(err); e.Field != tc.field || e.Code != tc.field {
			t.Fatalf("credential field: %+v", e)
		}
	}
}
