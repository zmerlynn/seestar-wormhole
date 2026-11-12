package cloudhttp

import (
	"fmt"
	"log/slog"

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
// Only Multiple permits the GUI to offer a picker. Error, Format and LogValue
// name the received telescopes by their escaped labels; nothing else from the
// cloud reply reaches them.
type SelectionError struct {
	cause    error
	choices  []DeviceChoice
	Multiple bool
}

func (e *SelectionError) Unwrap() error           { return e.cause }
func (e *SelectionError) Choices() []DeviceChoice { return append([]DeviceChoice(nil), e.choices...) }

// Error is the cause followed by the received telescopes on the same line,
// within the catalog message budget.
func (e *SelectionError) Error() string {
	s := e.cause.Error()
	if len(e.choices) == 0 {
		return s
	}
	const received = "; received telescopes: "
	labels := make([]string, len(e.choices))
	for i, d := range e.choices {
		labels[i] = d.Label
	}
	return s + received + joinLabels(len(s)+len(received), labels, len(labels))
}

// CLIMessage lists the received telescopes a line each for the account owner.
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

func (e *SelectionError) LogValue() slog.Value          { return slog.StringValue(e.Error()) }
func (e *SelectionError) Format(s fmt.State, verb rune) { fmt.Fprint(s, e.Error()) }

// ChoiceLabel combines escaped model text and the full account-owner serial.
func (d Device) ChoiceLabel() string {
	return displayModel(d.Model) + " " + displaySerial(d.Serial)
}

func selectionChoices(devices []cloud.Detail) []DeviceChoice {
	var choices []DeviceChoice
	for _, d := range devices {
		if !ValidSerial(d.Serial()) {
			continue
		}
		choices = append(choices, DeviceChoice{Serial: d.Serial(), Model: d.Model(), Label: (Device{Serial: d.Serial(), Model: d.Model()}).ChoiceLabel()})
	}
	return choices
}
