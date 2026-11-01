package cloudhttp

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestSerialLabelASCIIEscaping(t *testing.T) {
	for _, tc := range []struct{ name, character, escaped string }{
		{"bidi override", "\u202e", `\u202e`},
		{"bidi isolate", "\u2066", `\u2066`},
		{"zero width space", "\u200b", `\u200b`},
		{"zero width joiner", "\u200d", `\u200d`},
		{"line separator", "\u2028", `\u2028`},
		{"paragraph separator", "\u2029", `\u2029`},
		{"nonbreaking space", "\u00a0", `\u00a0`},
		{"byte order mark", "\ufeff", `\ufeff`},
		{"nonprinting", "\u0085", `\u0085`},
		{"non ASCII printable", "\u00e9", `\u00e9`},
		{"quote", "\"", `\"`},
		{"backslash", "\\", `\\`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serial := "synthetic-label-" + tc.character + "1234"
			encoded, err := json.Marshal(serial)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(twoDevices, `"other-serial-sentinel-5678"`, string(encoded), 1)
			devices, _, err := cloud.ParseDevices([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			choices := selectionChoices(devices)
			if len(choices) != 2 {
				t.Fatal("valid serial omitted")
			}
			t.Run("wire", func(t *testing.T) {
				if choices[1].Serial != serial {
					t.Fatal("display escaping changed the selection identity")
				}
			})
			t.Run("label", func(t *testing.T) {
				if choices[1].Label != "S30 synthetic-label-"+tc.escaped+"1234" {
					t.Fatalf("unsafe serial label: %q", choices[1].Label)
				}
			})
		})
	}
}
