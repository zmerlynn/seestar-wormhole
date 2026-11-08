package main

import (
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func registerSharedOptions(f *pflag.FlagSet, o *options, l *logOptions) {
	f.StringVar(&o.serial, "serial", "", "optional device serial: 1-64 printable ASCII bytes (0x21-0x7E; no spaces); required for accounts with several devices")
	f.StringVar(&o.model, "model", "", "device model; required when a serial has several catalog models")
	f.StringVar(&o.stateDir, "state-dir", "", "private installation UUID storage directory (default: memory only; UI state root with --config)")
	registerLoggingOptions(f, l, "json")
	registerVersionOption(f, o)
}

// logOptions holds one command's --log-format and --log-level. Each command
// has its own: pflag writes a flag's default into its variable when the flag
// is registered, so commands sharing a variable would share the last default.
type logOptions struct{ format, level string }

// registerLoggingOptions registers --log-format, with the command's default
// format, and --log-level.
func registerLoggingOptions(f *pflag.FlagSet, l *logOptions, format string) {
	f.StringVar(&l.format, "log-format", format, "stderr log format: json or text")
	f.StringVar(&l.level, "log-level", "info", "stderr log level: debug, info, warn or error")
}

func registerVersionOption(f *pflag.FlagSet, o *options) {
	f.BoolVar(&o.version, "version", false, "print version, revision and platform")
}

// use makes l the logging options of the run.
func (l *logOptions) use(o *options) { o.format, o.level = l.format, l.level }

func validateSharedOptions(o *options) error {
	if err := validateLoggingOptions(o); err != nil {
		return err
	}
	if o.serial != "" && !cloudhttp.ValidSerial(o.serial) {
		return &argumentError{Option: "--serial", Rule: cloudhttp.SerialRule}
	}
	if o.model != "" && !cloudhttp.ValidModel(o.model) {
		return &argumentError{Option: "--model", Rule: cloudhttp.ModelRule}
	}
	return nil
}

func validateLoggingOptions(o *options) error {
	if o.format != "json" && o.format != "text" {
		return &argumentError{Option: "--log-format", Rule: "choose json or text"}
	}
	if _, err := diagnostic.Level(o.level); err != nil {
		return &argumentError{Option: "--log-level", Rule: "choose debug, info, warn or error"}
	}
	return nil
}
