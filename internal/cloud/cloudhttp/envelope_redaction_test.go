package cloudhttp

import (
	"bytes"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"log/slog"
	"strings"
	"testing"
)

func TestEnvelopeValuesAreRedacted(t *testing.T) {
	for _, body := range []string{`{"code":87654321,"data":{"accessToken":false}}`, `{"code":200,"code":87654321,"data":null}`} {
		var sink bytes.Buffer
		c := &Client{logger: redactionTestLogger(&sink, slog.LevelDebug)}
		c.rejected(t.Context(), "login", responseInfo{status: 200}, []byte(body), cloud.ErrInvalid)
		if strings.Contains(sink.String(), "87654321") {
			t.Errorf("numeric response sentinel copied into debug event: %s", sink.String())
		}
	}
}
