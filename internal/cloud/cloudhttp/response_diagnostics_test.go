package cloudhttp

import (
	"bytes"
	"github.com/go-json-experiment/json"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestRejectedResponseUsesFixedMetadata(t *testing.T) {
	for _, body := range []string{
		rejectedResponse,
		`{"password":"malformed-secret"`,
		`{} {"password":"trailing-secret"}`,
		strings.Repeat(" ", cloud.MaxBody+1),
	} {
		var logs bytes.Buffer
		c := &Client{logger: redactionTestLogger(&logs, slog.LevelDebug)}
		c.rejected(t.Context(), "login", responseInfo{status: 502, contentType: `text/html; secret=header-secret`}, []byte(body), cloud.ErrTransport)
		var got map[string]any
		if err := json.Unmarshal(logs.Bytes(), &got, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"level":            "DEBUG",
			"msg":              "cloud_response_rejected",
			"phase":            "login",
			"http_status":      float64(502),
			"content_type":     "text/html",
			"body_length":      float64(len(body)),
			"body_truncated":   len(body) > cloud.MaxBody,
			"validation_field": "http_status",
			"validation_rule":  "accepted_status",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("diagnostic = %v, want %v", got, want)
		}
	}
}
