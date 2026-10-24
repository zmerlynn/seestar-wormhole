package proxy

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

func TestLicenseOmissionUsesOwnerLogger(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		for _, tc := range []struct {
			valid   bool
			omitted int
		}{{false, 3}, {true, 0}, {true, 1}, {true, 3}} {
			for _, level := range []slog.Level{slog.LevelInfo, slog.LevelDebug} {
				var logs bytes.Buffer
				f := &factory{s: &Session{logger: diagnostic.Logger(format, &logs, level)}, verify: func(string) (iotc.License, error) {
					l := iotc.License{Realm: "secret-realm", StandaloneOmitted: tc.omitted}
					if !tc.valid {
						return l, errors.New("secret-error")
					}
					return l, nil
				}}
				_, err := f.license("secret-license")
				if (err == nil) != tc.valid {
					t.Fatal("verification outcome changed")
				}
				want := tc.valid && tc.omitted > 0 && level == slog.LevelDebug
				if (logs.Len() > 0) != want {
					t.Fatalf("format=%s valid=%v omitted=%d level=%v: diagnostic=%s", format, tc.valid, tc.omitted, level, &logs)
				}
				if want {
					if !strings.Contains(logs.String(), "iotc license standalone entries omitted") {
						t.Fatalf("missing omission diagnostic: %s", &logs)
					}
					if format == "json" {
						var record struct{ Count int }
						if err := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
							t.Fatal(err)
						}
						if record.Count != tc.omitted {
							t.Fatalf("omission count=%d, want %d", record.Count, tc.omitted)
						}
					} else if !slices.Contains(strings.Fields(logs.String()), fmt.Sprintf("count=%d", tc.omitted)) {
						t.Fatalf("missing exact omission count %d: %s", tc.omitted, &logs)
					}
				}
				if strings.Contains(logs.String(), "secret-") {
					t.Fatal("license material leaked")
				}
			}
		}
	}
	f := &factory{s: &Session{}, verify: func(string) (iotc.License, error) { return iotc.License{StandaloneOmitted: 1}, nil }}
	if _, err := f.license(""); err != nil {
		t.Fatal(err)
	}
}

func TestDialLicenseOmissionUsesOwnerLogger(t *testing.T) {
	var owned, processDefault bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(diagnostic.Logger("json", &processDefault, slog.LevelDebug))
	t.Cleanup(func() { slog.SetDefault(previous) })
	calls := 0
	f := &factory{
		s: &Session{logger: diagnostic.Logger("json", &owned, slog.LevelDebug)},
		verify: func(string) (iotc.License, error) {
			calls++
			return iotc.License{
				Realm: "secret-realm", Mask: [16]byte{'s', 'e', 'c', 'r', 'e', 't'},
				Standalone: map[string]string{"secret-name": "secret-address"}, StandaloneOmitted: 3,
			}, nil
		},
	}
	// An empty lease stops at UID validation, after production license handling,
	// before transport I/O or worker startup.
	x := &stack{f: f}
	if _, err := x.Dial(t.Context(), cloudLease{Lease: new(cloudhttp.Lease)}); err == nil {
		t.Fatal("empty UID accepted")
	}
	if calls != 1 {
		t.Fatalf("verifier calls=%d want 1", calls)
	}
	logText := owned.String()
	decoder := jsontext.NewDecoder(strings.NewReader(logText))
	found := 0
	for {
		var record struct {
			Msg, Level string
			Count      int
		}
		err := json.UnmarshalDecode(decoder, &record, json.MatchCaseInsensitiveNames(true))
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if record.Msg == "iotc license standalone entries omitted" {
			found++
			if record.Count != 3 || record.Level != "DEBUG" {
				t.Errorf("omission count=%d level=%s want 3/DEBUG", record.Count, record.Level)
			}
		}
	}
	if found != 1 {
		t.Errorf("omission records=%d want 1", found)
	}
	if processDefault.Len() != 0 {
		t.Error("Dial wrote to the process-default logger")
	}
	for _, secret := range []string{"secret-", "[115 101 99 114 101 116", "[115,101,99,114,101,116"} {
		if strings.Contains(logText, secret) || strings.Contains(processDefault.String(), secret) {
			t.Error("Dial logged license material")
		}
	}
}
