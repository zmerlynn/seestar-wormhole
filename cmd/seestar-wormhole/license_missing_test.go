package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"

	"github.com/rclone/rclone/fs"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestLicenseMissingCommandProjection(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			_, _, raw := cloud.ParseOnline([]byte(`{"data":{"conId":"PRIVATE_ID","deviceUid":"PRIVATE_UID","username":"PRIVATE_USER","password":"PRIVATE_PASS"}}`))
			newFailure := func() error { return diagnostic.AtLayer(raw, session.Cloud, session.ErrUncertain) }
			for _, command := range []commandKind{commandProxy, commandPull} {
				if code := commandExitCode(options{command: command}, newFailure()); code != 1 {
					t.Errorf("command=%v exit=%d, want 1", command, code)
				}
			}
			var out, logs bytes.Buffer
			err := runPull(context.Background(), []string{"pull", "--output=" + format, "--log-format=" + format, t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return nil, newFailure() })
			if pullCode(err) != 1 {
				t.Errorf("exit=%d want 1", pullCode(err))
			}
			if format == "json" {
				assertJSONSummary(t, out.String(), 1, "error", "cloud_license_missing", "none")
			} else if !strings.Contains(out.String(), "reason=cloud_license_missing ") || !strings.Contains(out.String(), "cleanup_reason=none") {
				t.Errorf("unexpected text summary: %s", &out)
			}
			if !strings.Contains(logs.String(), "service-side change, not your configuration") {
				t.Errorf("missing diagnostic hint: %s", &logs)
			}
			// Proxy owner reports use Log; startup/final reports use LogFinal.
			logger := diagnostic.Logger(format, &logs, slog.LevelDebug)
			diagnostic.Log(logger, "layer_failed", "session", newFailure())
			diagnostic.LogFinal(logger, "proxy failed", "session", newFailure(), nil)
			if strings.Count(logs.String(), "service-side change, not your configuration") < 3 || strings.Contains(logs.String()+out.String(), "PRIVATE_") {
				t.Errorf("missing hints or leaked allocation material: %s %s", &logs, &out)
			}
			// A completed owner can also retain the blocking release marker.
			// It must not replace the primary reason in the pull summary.
			out.Reset()
			logs.Reset()
			primary := newFailure()
			s := &pullSessionFixture{closeErr: diagnostic.RetainTerminal(primary, session.ErrStop)}
			err = runPullWithSource(t.Context(), []string{"pull", "--output=" + format, t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					return ctx, nil, primary
				})
			if pullCode(err) != 1 || s.closed != 1 {
				t.Errorf("exit=%d closes=%d, want 1/1", pullCode(err), s.closed)
			}
			if format == "json" {
				assertJSONSummary(t, out.String(), 1, "error", "cloud_license_missing", "release_failed")
			} else if !strings.Contains(out.String(), "reason=cloud_license_missing cleanup_reason=release_failed") {
				t.Errorf("unexpected owner summary: %s", &out)
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
