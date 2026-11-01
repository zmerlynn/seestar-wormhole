package cloudhttp

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func TestSelectionChoices(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		input      Device
		multiple   bool
	}{
		{"multiple", twoDevices, Device{}, true},
		{"model only", twoDevices, Device{Model: "S30"}, true},
		{"repeated serial", pairPage, Device{}, true},
		{"ambiguous serial", pairPage, Device{Serial: "private-a"}, false},
		{"incomplete", strings.Replace(twoDevices, `"total":2`, `"total":0`, 1), Device{}, true},
		{"duplicate", strings.Replace(twoDevices, `"other-serial-sentinel-5678","deviceModel":"S30"`, `"serial-sentinel-secret-1234","deviceModel":"S50 Pro"`, 1), Device{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			c := selectionClient(t, &selectionCloud{bodies: []string{tc.body}}, &logs, slog.LevelDebug)
			_, err := c.SelectDevice(t.Context(), tc.input)
			var selection *SelectionError
			if !errors.As(err, &selection) {
				t.Fatalf("no structured choices: %v", err)
			}
			if selection.Multiple != tc.multiple {
				t.Fatalf("multiple=%v", selection.Multiple)
			}
			want := []string{"S50 Pro serial-sentinel-secret-1234", "S30 other-serial-sentinel-5678"}
			if strings.Contains(tc.name, "serial") {
				want = []string{"S50 Pro private-a", "S30 private-a"}
			}
			if tc.name == "duplicate" {
				want = want[:1]
			}
			choices := selection.Choices()
			if len(choices) != len(want) {
				t.Fatalf("choices=%v", choices)
			}
			for i, line := range want {
				if choices[i].Label != line || !strings.Contains(err.Error(), "\n"+line) {
					t.Fatalf("missing full pair %q in %s", line, err.Error())
				}
			}
			if !strings.Contains(err.Error(), "\nReceived telescopes (full serials):\n") || !strings.Contains(err.Error(), "Pass --serial; also pass --model when a serial appears under more than one model.") {
				t.Fatal("missing selection instructions")
			}
			diagnostic.Log(c.logger, "failure", "", err)
			assertSelectionSecrets(t, logs.String())
			if strings.Contains(logs.String(), "private-a") {
				t.Fatal("catalog in logs")
			}
		})
	}
}

func TestSelectionChoiceUntrustedText(t *testing.T) {
	const hostile = "<img src=x>\x1b\t\n" // synthetic server text; exact model is selection authority
	body := strings.Replace(twoDevices, `"S50 Pro"`, `"<img src=x>\u001b\t\n"`, 1)
	body = strings.Replace(body, "other-serial-sentinel-5678", `invalid\nserial`, 1)
	var logs bytes.Buffer
	c := selectionClient(t, &selectionCloud{bodies: []string{body}}, &logs, slog.LevelInfo)
	_, err := c.SelectDevice(t.Context(), Device{})
	var selection *SelectionError
	if !errors.As(err, &selection) {
		t.Fatal("no structured list")
	}
	choices := selection.Choices()
	if len(choices) != 1 || choices[0].Serial != "serial-sentinel-secret-1234" || choices[0].Model != hostile || choices[0].Label != `"<img src=x>\x1b\t\n" serial-sentinel-secret-1234` {
		t.Fatalf("untrusted choices=%q", choices)
	}
	if strings.Contains(err.Error(), "invalid") || strings.ContainsAny(choices[0].Label, "\x1b\t\n") {
		t.Fatal("unsafe choice text")
	}
	// Also bound unknown model display, preserving its complete wire value.
	long := strings.Repeat("x", 200)
	devices, _, parseErr := cloud.ParseDevices([]byte(strings.Replace(twoDevices, "S50 Pro", long, 1)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	bounded := selectionChoices(devices)
	if bounded[0].Model != long || bounded[0].Label != `"`+strings.Repeat("x", 96)+`\u2026" serial-sentinel-secret-1234` {
		t.Fatal("model bound or wire value lost")
	}
}

func TestSelectionChoiceSerialDisplay(t *testing.T) {
	for _, tc := range []struct {
		serial string
		count  int
		label  string
	}{
		{"synthetic-serial\r", 1, ""},
		{strings.Repeat("x", 4097), 1, ""},
		{"synthetic-serial\x1b", 2, `S30 "synthetic-serial\x1b"`},
	} {
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
			t.Fatalf("invalid serial retained: count=%d", len(choices))
		}
		if tc.label != "" && (choices[1].Label != tc.label || choices[1].Serial != tc.serial) {
			t.Fatalf("serial display or wire value lost: %q", choices[1])
		}
	}
}
