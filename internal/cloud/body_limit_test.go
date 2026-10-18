package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestResponseBodyLimit(t *testing.T) {
	for _, parser := range []struct {
		name  string
		body  string
		parse func([]byte) error
	}{
		{"stop", `{"code":200}`, ParseStop},
		{"online", onlineFixture, func(b []byte) error { _, _, err := ParseOnline(b); return err }},
		{"detail", detailFixture, func(b []byte) error { _, err := ParseDetail(b); return err }},
		{"LAN detail", detailFixture, func(b []byte) error { _, err := ParseLANDetail(b); return err }},
		{"devices", devicesFixture, func(b []byte) error { _, _, err := ParseDevices(b); return err }},
		{"devices with logger", devicesFixture, func(b []byte) error { _, _, err := ParseDevicesWithLogger(b, nil); return err }},
		{"device page with logger", devicesFixture, func(b []byte) error { _, err := ParseDevicePageWithLogger(b, nil); return err }},
		{"login", loginFixture, func(b []byte) error { _, err := ParseLogin(200, b, false); return err }},
		{"renewal", loginFixture, func(b []byte) error { _, err := ParseLogin(201, b, true); return err }},
		{"profile 200", `{"data":{"id":"profile"}}`, func(b []byte) error { return ParseProfile(200, b) }},
		{"profile 201", `{"data":{"id":"profile"}}`, func(b []byte) error { return ParseProfile(201, b) }},
		{"profile 204", `{"data":{"id":"profile"}}`, func(b []byte) error { return ParseProfile(204, b) }},
	} {
		t.Run(parser.name, func(t *testing.T) {
			for _, boundary := range []struct {
				name string
				size int
			}{
				{"at limit", MaxBody},
				{"one byte over", MaxBody + 1},
			} {
				t.Run(boundary.name, func(t *testing.T) {
					// Whitespace keeps both bodies valid JSON with usable fields,
					// so only the byte limit can reject the oversized response.
					body := parser.body + strings.Repeat(" ", boundary.size-len(parser.body))
					err := parser.parse([]byte(body))
					if boundary.size == MaxBody {
						if err != nil {
							t.Fatalf("%d-byte response: got %v, want success", len(body), err)
						}
						return
					}
					if !errors.Is(err, ErrInvalid) {
						t.Fatalf("%d-byte response: got %v, want ErrInvalid", len(body), err)
					}
					var validation *ValidationError
					if !errors.As(err, &validation) || validation.Field != "body" || validation.Rule != "body_limit" {
						t.Fatalf("%d-byte response: got %v, want body/body_limit validation", len(body), err)
					}
				})
			}
		})
	}
}
