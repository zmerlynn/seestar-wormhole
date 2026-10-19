package edge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	profile "github.com/zmerlynn/seestar-wormhole/internal/dtls"
)

func TestCredentialNULRefusal(t *testing.T) {
	for _, tc := range []struct{ name, user, password string }{
		{"username", "user\x00tail", "password"},
		{"password", "user", "password\x00tail"},
		{"leading", "\x00user", "password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			p, _ := NewDTLSPackets(1388)
			p.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
			_, err := DialDTLS(context.Background(), p, []byte(tc.user), []byte(tc.password))
			if !errors.Is(err, profile.ErrCredentialNUL) {
				t.Fatalf("error = %v, want NUL refusal", err)
			}
			if !strings.Contains(logs.String(), `"level":"WARN"`) || !strings.Contains(logs.String(), `"rule":"R024"`) {
				t.Fatalf("missing rule WARN: %s", &logs)
			}
			if strings.Contains(logs.String(), "user") || strings.Contains(logs.String(), "password") {
				t.Fatalf("credential leaked: %s", &logs)
			}
		})
	}
}
