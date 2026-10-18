package proxy

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

func TestLicenseNULRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, plain string
		refused     bool
	}{
		{"leading", "\x00secret-license", true},
		{"embedded", "secret\x00license", true},
		{"trailing", "secret-license\x00", true},
		{"unchanged", "secret-license", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			var recovered [128]byte
			recovered[0] = 0x42 // Check decoded NUL, not NUL in the masked bytes.
			payload := []byte(tc.plain)
			for i := range payload {
				payload[i] ^= recovered[i%32]
			}
			f := &factory{s: &Session{logger: slog.New(slog.NewJSONHandler(&logs, nil))}, verify: func(string) (iotc.License, error) {
				plain, err := iotc.UnmaskLicense(payload, recovered)
				if err == nil && string(plain) != tc.plain {
					t.Fatalf("unmask changed bytes: %q", plain)
				}
				if err != nil && plain != nil {
					t.Fatal("refused license exposed a prefix")
				}
				return iotc.License{}, err
			}}
			_, err := f.license("secret-envelope")
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "NUL") {
					t.Fatalf("missing NUL error: %v", err)
				}
				for _, want := range []string{`"level":"WARN"`, `"rule":"license_nul"`} {
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("missing %s: %s", want, &logs)
					}
				}
			} else if err != nil || logs.Len() != 0 {
				t.Fatalf("valid unmask: %v logs=%s", err, &logs)
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("license material logged")
			}
		})
	}
}
