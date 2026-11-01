package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestSerialBound(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"empty", "", false}, {"one", "!", true},
		{"63", strings.Repeat("s", 63), true}, {"64", strings.Repeat("s", 64), true},
		{"65", strings.Repeat("s", 65), false}, {"former Unicode fixture", strings.Repeat("\u202e", 11), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if ValidSerial(tc.value) != tc.valid {
				t.Fatalf("ValidSerial=%v want %v", ValidSerial(tc.value), tc.valid)
			}
		})
	}
	// Exercise every byte, including 0x20, 0x7f and invalid UTF-8 0x80.
	for b := 0; b <= 255; b++ {
		t.Run(strconv.Itoa(b), func(t *testing.T) {
			want := b >= 0x21 && b <= 0x7e
			if got := ValidSerial("s" + string([]byte{byte(b)})); got != want {
				t.Fatalf("byte 0x%02x accepted=%v want=%v", b, got, want)
			}
		})
	}
	// The model and other opaque inputs retain their independent bound.
	if !ValidModel(strings.Repeat("m", 4096)) || ValidModel(strings.Repeat("m", 4097)) || !validInput("opaque \t\u202e") {
		t.Fatal("generic input policy changed")
	}
}

func TestSerialBoundSelection(t *testing.T) {
	for _, serial := range []string{strings.Repeat("s", 65), strings.Repeat("\u202e", 11), "s ", "s\x7f", "s\x80"} {
		t.Run(strconv.QuoteToASCII(serial), func(t *testing.T) {
			var logs bytes.Buffer
			f := &selectionCloud{}
			c := selectionClient(t, f, &logs, slog.LevelInfo)
			_, err := c.SelectDevice(t.Context(), Device{Serial: serial, Model: "S30"})
			var option *diagnostic.OptionError
			if !errors.Is(err, cloud.ErrConfig) || !errors.As(err, &option) || option.Option != "--serial" || option.Rule != SerialRule {
				t.Fatalf("wrong refusal: %v", err)
			}
			if len(f.queries) != 0 || strings.Contains(logs.String(), "device_selected") {
				t.Fatal("invalid input reached selection")
			}
		})
	}
}

func TestSerialBoundCatalogChoices(t *testing.T) {
	for _, tc := range []struct {
		serial string
		count  int
	}{
		{strings.Repeat("s", 63), 2}, {strings.Repeat("s", 64), 2}, {strings.Repeat("s", 65), 1},
		{strings.Repeat("\u202e", 11), 1}, {"s ", 1}, {"s\x7f", 1}, {"s\u0080", 1},
	} {
		t.Run(strconv.QuoteToASCII(tc.serial), func(t *testing.T) {
			encoded, err := json.Marshal(tc.serial)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(twoDevices, `"other-serial-sentinel-5678"`, string(encoded), 1)
			devices, _, err := cloud.ParseDevices([]byte(body))
			if err != nil {
				t.Fatal(err)
			}
			choices := selectionChoices(devices)
			if len(choices) != tc.count {
				t.Fatalf("choices=%d want=%d", len(choices), tc.count)
			}
			if tc.count == 2 && choices[1].Serial != tc.serial {
				t.Fatal("wire serial changed")
			}
		})
	}
}
