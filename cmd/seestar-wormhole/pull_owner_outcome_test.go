package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/rclone/rclone/backend/local"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/pull"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

func TestPullRetainsOwnerOutcome(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		for _, release := range []string{"after-work", "deferred"} {
			for _, reported := range []bool{false, true} {
				for _, tc := range []struct {
					cleanup error
					reason  string
				}{{nil, "none"}, {session.ErrStop, "release_failed"}} {
					cleanup, cleanupReason := tc.cleanup, tc.reason
					name := format + "/" + release + "/" + map[bool]string{false: "unreported", true: "reported"}[reported] + "/" + cleanupReason
					t.Run(name, func(t *testing.T) {
						primary := diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)
						outcome := errors.Join(context.Canceled, primary, cleanup)
						s := &pullSessionFixture{closeErr: outcome}
						root, dest := t.TempDir(), t.TempDir()
						var out, logs bytes.Buffer
						err := runPullWithSource(t.Context(), []string{"pull", "--output=" + format, "--log-format=" + format, dest}, &out, &logs, keylog.Open,
							func(_ context.Context, cfg proxy.Config) (commandSession, error) {
								if reported {
									diagnostic.Log(cfg.Logger, "layer_failed", "session", primary)
								}
								return s, nil
							},
							func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
								if release == "deferred" {
									return ctx, nil, context.Canceled
								}
								source, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
								return ctx, source, err
							})
						if pullCode(err) != 1 || s.closed != 1 {
							t.Errorf("exit=%d closes=%d, want exit=1 closes=1", pullCode(err), s.closed)
						}
						for _, cause := range []error{outcome, primary, context.Canceled, cleanup} {
							if cause != nil && !errors.Is(err, cause) {
								t.Errorf("lost owner cause %v from %v", cause, err)
							}
						}
						if phase, reason := diagnostic.Fields(err, "pull"); phase != "rdt" || reason != "timeout" {
							t.Errorf("primary=%s/%s, want rdt/timeout", phase, reason)
						}
						if format == "json" {
							assertJSONSummary(t, out.String(), 1, "error", "timeout", cleanupReason)
						} else if strings.Count(out.String(), "\n") != 1 || !strings.HasPrefix(out.String(), "status=error ") || !strings.HasSuffix(out.String(), " reason=timeout cleanup_reason="+cleanupReason+"\n") {
							t.Errorf("text summary=%s, want error with timeout and cleanup=%s", &out, cleanupReason)
						}
						primaryFields := "phase=rdt reason=timeout"
						cleanupFields := "phase=release reason=release_failed cleanup_reason=release_failed"
						finalMessage := `msg="pull failed"`
						if format == "json" {
							primaryFields = `"phase":"rdt","reason":"timeout"`
							cleanupFields = `"phase":"release","reason":"release_failed","cleanup_reason":"release_failed"`
							finalMessage = `"msg":"pull failed"`
						}
						if got := strings.Count(logs.String(), primaryFields); got != 1 {
							t.Errorf("primary reports=%d, want 1: %s", got, &logs)
						}
						wantFinal := 1
						if reported && cleanup == nil {
							wantFinal = 0
						}
						if got := strings.Count(logs.String(), finalMessage); got != wantFinal {
							t.Errorf("final reports=%d, want %d: %s", got, wantFinal, &logs)
						}
						if reported && cleanup != nil && !strings.Contains(logs.String(), cleanupFields) {
							t.Errorf("missing independent release report: %s", &logs)
						}
						// A retained primary must prevent checkpoint advancement even
						// when the batch itself completed and release succeeded.
						d, openErr := pull.OpenDestination(dest)
						if openErr != nil {
							t.Fatal(openErr)
						}
						defer d.Close()
						if len(d.State.Selections) != 0 {
							t.Errorf("failed owner outcome advanced checkpoints: %+v", d.State.Selections)
						}
					})
				}
			}
		}
	}
}

func TestPullOwnerExpectedCancellation(t *testing.T) {
	for _, closeErr := range []error{
		context.Canceled,
		errors.Join(context.Canceled, session.ErrTransport),
		diagnostic.NewTerminal(session.RDT, "canceled", session.ErrTransport),
		session.ErrBusy,
	} {
		t.Run(closeErr.Error(), func(t *testing.T) {
			root := t.TempDir()
			s := &pullSessionFixture{closeErr: closeErr}
			var out, logs bytes.Buffer
			err := runPullWithSource(t.Context(), []string{"pull", "--output=json", t.TempDir()}, &out, &logs, keylog.Open,
				func(context.Context, proxy.Config) (commandSession, error) { return s, nil },
				func(ctx context.Context, _ func(context.Context, string, string) (net.Conn, error)) (context.Context, fs.Fs, error) {
					source, err := local.NewFs(ctx, "fixture", root, configmap.Simple{})
					return ctx, source, err
				})
			if err != nil || s.closed != 1 || logs.Len() != 0 {
				t.Fatalf("err=%v closes=%d logs=%s, want success and one quiet close", err, s.closed, &logs)
			}
			assertJSONSummary(t, out.String(), 0, "nothing-new", "none", "none")
		})
	}
}
