package cloudhttp

import (
	"fmt"
	"log/slog"
	"strconv"

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
// rather than include choices. Generic error projections never include account data.
// The private cause carries fixed guidance; received identities live in choices.
type SelectionError struct {
	cause    error
	choices  []DeviceChoice
	Multiple bool
}

func (e *SelectionError) Unwrap() error           { return e.cause }
func (e *SelectionError) Choices() []DeviceChoice { return append([]DeviceChoice(nil), e.choices...) }
func (e *SelectionError) Error() string           { return e.cause.Error() }

// CLIMessage explicitly includes full received pairs for account-owner stdout.
// It must never be used for diagnostics or generic error formatting.
func (e *SelectionError) CLIMessage() string {
	s := e.cause.Error()
	if len(e.choices) == 0 {
		return s
	}
	s += "\nReceived telescopes (full serials):"
	for _, d := range e.choices {
		s += "\n" + d.Label
	}
	if e.Multiple {
		s += "\nPass --serial with one of these."
	}
	return s
}

// Generic formatting and logging share the sanitized message, never the list.
func (e *SelectionError) LogValue() slog.Value          { return slog.StringValue(e.Error()) }
func (e *SelectionError) Format(s fmt.State, verb rune) { fmt.Fprint(s, e.Error()) }

func selectionChoices(devices []cloud.Detail) []DeviceChoice {
	var choices []DeviceChoice
	for _, d := range devices {
		if !ValidSerial(d.Serial()) {
			continue
		}
		model := displayModel(d.Model())
		serial := strconv.QuoteToASCII(d.Serial())
		serial = serial[1 : len(serial)-1]
		choices = append(choices, DeviceChoice{Serial: d.Serial(), Model: d.Model(), Label: model + " " + serial})
	}
	return choices
}
