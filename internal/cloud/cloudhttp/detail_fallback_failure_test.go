package cloudhttp

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestFallbackPreservesFailedDetailWithMismatchedIdentity(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"vendor refusal with other identity", strings.Replace(detail, `"code":200`, `"code":300405`, 1)},
		{"malformed status with other identity", strings.Replace(detail, `"deviceStatus":1`, `"deviceStatus":"broken"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{pageA1}, detailBody: tc.body}, &logs, slog.LevelDebug)
			request := Device{"missing-synthetic-serial", "S50 Pro"}
			_, direct := c.Detail(t.Context(), request)
			_, fallback := c.SelectDevice(t.Context(), request)
			if !errors.Is(direct, cloud.ErrInvalid) {
				t.Fatalf("fixture did not fail detail: %v", direct)
			}
			dp, dr := diagnostic.Fields(direct, "")
			fp, fr := diagnostic.Fields(fallback, "")
			if errors.Is(fallback, cloud.ErrConfig) || dp != fp || dr != fr {
				t.Errorf("detail failure became selection refusal: direct=%s/%s fallback=%s/%s error=%v", dp, dr, fp, fr, fallback)
			}
		})
	}
}
