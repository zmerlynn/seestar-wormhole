package capturetrace

import (
	"errors"
	"strings"
	"testing"
)

const testKeyLine = "# IOTC_KEY 1 53594e5448455449 3 11111111111111111111111111111111 333333333333333333333333\n"

func TestKeySelection(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		attempt    uint64
		present    bool
		bad        bool
	}{
		{"selected", testKeyLine, 1, true, false},
		{"different attempt", testKeyLine, 2, false, false},
		{"different reference", strings.Replace(testKeyLine, " 3 ", " 4 ", 1), 1, false, false},
		{"repeat", testKeyLine + testKeyLine, 1, true, false},
		{"NSS ignored", "CLIENT_RANDOM synthetic test\n" + testKeyLine, 1, true, false},
		{"short key", strings.Replace(testKeyLine, strings.Repeat("11", 16), "11", 1), 1, false, true},
		{"short IV", strings.Replace(testKeyLine, strings.Repeat("33", 12), "33", 1), 1, false, true},
		{"short RID", strings.Replace(testKeyLine, "53594e5448455449", "53", 1), 1, false, true},
		{"bad attempt", strings.Replace(testKeyLine, " 1 ", " SECRET ", 1), 1, false, true},
		{"conflict", testKeyLine + strings.Replace(testKeyLine, strings.Repeat("11", 16), strings.Repeat("22", 16), 1), 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readKey(strings.NewReader(tc.line), 1024, tc.attempt)
			if tc.bad {
				var bad *DecodeError
				if !errors.As(err, &bad) || bad.Kind != MalformedKeylog {
					t.Fatalf("got %v", err)
				}
				if strings.Contains(err.Error(), "SECRET") {
					t.Fatal("keylog escaped")
				}
				return
			}
			if err != nil || (got != nil) != tc.present {
				t.Fatalf("present=%v error=%v", got != nil, err)
			}
		})
	}
}
