package main

import (
	"errors"
	"io"
	"log/slog"

	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// argumentError contains only registered option names and fixed rules. Supplied
// values and the parser's formatted errors never cross this boundary.
type argumentError = diagnostic.OptionError

func flagParseError(err error) error {
	var detail *argumentError
	if errors.As(err, &detail) {
		return detail
	}
	var missing *pflag.ValueRequiredError
	if errors.As(err, &missing) {
		return &argumentError{Option: "--" + missing.GetFlag().Name, Rule: "requires a value"}
	}
	var invalid *pflag.InvalidValueError
	if errors.As(err, &invalid) {
		rule := "use a supported value"
		switch invalid.GetFlag().Value.Type() {
		case "duration":
			rule = "use a duration such as 2m or 0"
		case "int":
			rule = "use an integer byte count"
			if invalid.GetFlag().Name == "transfers" {
				rule = "use an integer from 1 through 8"
			}
		case "bool":
			rule = "use a boolean value"
		}
		return &argumentError{Option: "--" + invalid.GetFlag().Name, Rule: rule}
	}
	return &argumentError{Option: "arguments", Rule: "use only supported options"}
}

// configurationLogger projects only the typed setting and fixed rule. It is
// shared by parser and runtime configuration failures, regardless of log options.
func configurationLogger(out io.Writer, command string, detail *argumentError) *slog.Logger {
	return newLogger("json", out).With("option", detail.Option, "rule", detail.Rule,
		"help", "seestar-wormhole "+command+" --help")
}

func logConfigurationError(out io.Writer, command string, err error) {
	var detail *argumentError
	if errors.As(err, &detail) {
		logger := configurationLogger(out, command, detail)
		if command == "proxy" {
			logger.Error("proxy failed", "phase", "arguments", "reason", "invalid_option", "cleanup_reason", "none")
		} else {
			logger.Error(command+" failed", "phase", "arguments", "reason", "invalid_option")
		}
	}
}

func logArgumentError(out io.Writer, err error) { logConfigurationError(out, "proxy", err) }
