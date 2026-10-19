package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestLicenseMissingCommandProjection(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			_, _, raw := cloud.ParseOnline([]byte(`{"data":{"conId":"PRIVATE_ID","deviceUid":"PRIVATE_UID","username":"PRIVATE_USER","password":"PRIVATE_PASS"}}`))
			newFailure := func() error { return diagnostic.AtLayer(raw, session.Cloud, session.ErrUncertain) }
			for _, command := range []commandKind{commandProxy} {
				if code := commandExitCode(options{command: command}, newFailure()); code != 1 {
					t.Errorf("command=%v exit=%d, want 1", command, code)
				}
			}
			var logs bytes.Buffer
			// Proxy owner reports use Log; startup/final reports use LogFinal.
			logger := diagnostic.Logger(format, &logs, slog.LevelDebug)
			diagnostic.Log(logger, "layer_failed", "session", newFailure())
			diagnostic.LogFinal(logger, "proxy failed", "session", newFailure(), nil)
			if strings.Count(logs.String(), "service-side change, not your configuration") < 2 || strings.Contains(logs.String(), "PRIVATE_") {
				t.Errorf("missing hints or leaked allocation material: %s", &logs)
			}
			// A present malformed key keeps its reason and never reaches logs.
			_, _, malformed := cloud.ParseOnline([]byte(`{"data":{"conId":"id","deviceUid":"uid","sdkLicenseKey":"PRIVATE_LICENSE\u0000TAIL","username":"user","password":"pass"}}`))
			logs.Reset()
			diagnostic.LogFinal(logger, "proxy failed", "cloud", malformed, nil)
			if !strings.Contains(logs.String(), "invalid_response") || strings.Contains(logs.String(), "PRIVATE_") || strings.Contains(logs.String(), "TAIL") {
				t.Errorf("malformed license diagnostic changed or leaked: %s", &logs)
			}
		})
	}
}
