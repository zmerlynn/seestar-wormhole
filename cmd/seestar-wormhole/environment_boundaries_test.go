package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Environment and serial inputs are bounded and rejected with attributed errors.
func TestEnvironmentBoundaries(t *testing.T) {
	cases := []struct{ key, value string }{
		{"SEESTAR_EMAIL", "\xff@domain.invalid"}, {"SEESTAR_EMAIL", strings.Repeat("x", 4097)}, {"SEESTAR_EMAIL", "private\r@example.invalid"},
		{"SEESTAR_PASSWORD", strings.Repeat("😀", 17)}, {"SEESTAR_PASSWORD", strings.Repeat("x", 129)}, {"SEESTAR_PASSWORD", "abcdefg\xff"},
	}
	for _, key := range []string{"--serial"} {
		for _, v := range []string{" ", strings.Repeat("x", 4097), "PRIVATE_PAYLOAD\r", "PRIVATE_PAYLOAD\n"} {
			cases = append(cases, struct{ key, value string }{key, v})
		}
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.key, len(tc.value)), func(t *testing.T) {
			setValidCommandEnvironment(t)

			if tc.key == "SEESTAR_EMAIL" || tc.key == "SEESTAR_PASSWORD" {
				t.Setenv("SEESTAR_EMAIL", "test@example.invalid")
				t.Setenv("SEESTAR_PASSWORD", "12345678")
			}
			if tc.key != "--serial" {
				t.Setenv(tc.key, tc.value)
				if runtime.GOOS == "windows" && os.Getenv(tc.key) != tc.value {
					// Skip only when the actual UTF-16 environment round trip changed
					// these bytes. Representable WTF-8 must still reach validation.
					// TestCredentialFailureSetting in internal/cloud rejects the
					// original invalid UTF-8 directly on every platform.
					t.Skip("Windows UTF-16 environment changed the fixture bytes; direct credential validation covers the original bytes")
				}
			}
			var out, logs bytes.Buffer
			args := []string{"--idle=2m", "--bind=0.0.0.0", "--port", availableCommandPort(t) + ":13459", "--log-format=text", "--log-level=debug"}
			if tc.key == "--serial" {
				args = append(args, "--serial", tc.value)
			}
			err := run(context.Background(), append([]string{"proxy"}, args...), &out, &logs)
			var option *diagnostic.OptionError
			if !errors.As(err, &option) || option.Option != tc.key || option.Rule == "" {
				t.Fatalf("wrong error attribution for %s: %v", tc.key, err)
			}
			var rec map[string]any
			if e := json.Unmarshal(logs.Bytes(), &rec, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatalf("want single JSON error: %v %s", e, &logs)
			}
			if rec["option"] != tc.key || rec["rule"] == nil || rec["level"] != "ERROR" || out.Len() != 0 {
				t.Fatalf("wrong diagnostic: %v", rec)
			}
			assertNoSensitiveErrorDetails(t, err, "PRIVATE_", "12.3456789", "23.4567891", "Etc/UTC", "13459", "0.0.0.0")
			for _, v := range []string{"PRIVATE_", "12.3456789", "23.4567891", "Etc/UTC", "13459", "0.0.0.0"} {
				if bytes.Contains(logs.Bytes(), []byte(v)) {
					t.Fatalf("supplied value leaked: %s", &logs)
				}
			}
		})
	}
}
