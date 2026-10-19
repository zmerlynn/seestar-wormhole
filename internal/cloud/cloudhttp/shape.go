package cloudhttp

import (
	"context"
	"errors"
	"log/slog"
	"mime"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// responseInfo belongs to one exchange, including immutable account snapshots.
// Logged length is bytes read, capped at MaxBody+1; truncation marks a lower bound.
type responseInfo struct {
	status      int
	contentType string
	complete    bool // Read reached EOF; a later Close error cannot undo completion.
}

// Typed JSON decoding intentionally cannot inventory ignored member names.
// Only an already-consumed, correlated numeric status can be observed here.
func (c *Client) unknownDeviceStatus(ctx context.Context) {
	if c.logger == nil || c.oddityRecords == nil || !c.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	for {
		n := c.oddityRecords.Load()
		if n >= 64 {
			return
		}
		if c.oddityRecords.CompareAndSwap(n, n+1) {
			c.logger.DebugContext(ctx, "cloud_receive_oddity", "category", "unknown_device_status")
			return
		}
	}
}

func (c *Client) rejected(ctx context.Context, phase string, info responseInfo, body []byte, err error) {
	if err == nil || info.status == 0 || c.logger == nil || !c.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	field, rule := "http_status", "accepted_status"
	var v *cloud.ValidationError
	if errors.As(err, &v) {
		field, rule = v.Field, v.Rule
	}
	c.logger.DebugContext(ctx, "cloud_response_rejected",
		"phase", phase,
		"http_status", info.status,
		"content_type", contentType(info.contentType),
		"body_length", len(body),
		"body_truncated", len(body) > cloud.MaxBody,
		"validation_field", field,
		"validation_rule", rule,
	)
}

// Header parameters and arbitrary media types can contain secrets. Keep only
// public MIME labels; never pass a raw header or MIME parse error to the logger.
func contentType(raw string) string {
	if raw == "" {
		return "absent"
	}
	media, _, err := mime.ParseMediaType(raw)
	if err == nil {
		switch media {
		case "application/json", "text/json", "text/plain", "text/html", "application/octet-stream":
			return media
		}
	}
	return "other"
}
