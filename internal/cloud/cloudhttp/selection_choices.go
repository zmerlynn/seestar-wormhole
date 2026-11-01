package cloudhttp

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

// DeviceChoice is account data for selection, never a logging projection.
// Model retains the exact wire value; Label contains bounded, escaped model text.
type DeviceChoice struct {
	Serial string `json:"serial"`
	Model  string `json:"model"`
	Label  string `json:"label"`
}

// SelectionError adds received catalog pairs to an actionable selection failure.
// Only Multiple permits the GUI to offer a picker. Logs must classify Unwrap,
// rather than format Error, which deliberately contains full serials for the CLI.
type SelectionError struct {
	cause    error
	choices  []DeviceChoice
	Multiple bool
}

func (e *SelectionError) Unwrap() error           { return e.cause }
func (e *SelectionError) Choices() []DeviceChoice { return append([]DeviceChoice(nil), e.choices...) }
func (e *SelectionError) Error() string {
	s := e.cause.Error()
	if len(e.choices) == 0 {
		return s
	}
	s += "\nReceived telescopes (full serials):"
	for _, d := range e.choices {
		s += "\n" + d.Label
	}
	return s + "\nPass --serial; also pass --model when a serial appears under more than one model."
}

// Accidental structured logging still uses a fixed label, never Error().
func (*SelectionError) LogValue() slog.Value          { return slog.StringValue("[device selection required]") }
func (*SelectionError) Format(s fmt.State, verb rune) { fmt.Fprint(s, "[device selection required]") }

func selectionChoices(devices []cloud.Detail) []DeviceChoice {
	var choices []DeviceChoice
	for _, d := range devices {
		if !ValidSerial(d.Serial()) {
			continue
		}
		model := displayModel(d.Model())
		serial := d.Serial()
		if strings.ContainsFunc(serial, unicode.IsControl) {
			serial = strconv.QuoteToASCII(serial)
		}
		choices = append(choices, DeviceChoice{Serial: d.Serial(), Model: d.Model(), Label: model + " " + serial})
	}
	return choices
}
