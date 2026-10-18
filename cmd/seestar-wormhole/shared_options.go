package main

import (
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func registerSharedOptions(f *pflag.FlagSet, o *options) {
	f.StringVar(&o.trace, "trace", "", "new private redacted JSON Lines trace file (off by default)")
	f.StringVar(&o.keylog, "keylog", "", "new private capture key file (sensitive; off by default, best effort)")
	f.StringVar(&o.serial, "serial", "", "optional device serial; required for accounts with several devices")
	f.StringVar(&o.model, "model", "", "device model; required when a serial has several catalog models")
	f.StringVar(&o.stateDir, "state-dir", "", "private installation UUID storage directory (default empty: memory only)")
	f.StringVar(&o.format, "log-format", "json", "stderr log format: json or text")
	f.StringVar(&o.level, "log-level", "info", "stderr log level: debug, info, warn or error")
	f.BoolVar(&o.version, "version", false, "print version, revision and platform")
}

func validateSharedOptions(o *options) error {
	if o.format != "json" && o.format != "text" {
		return &argumentError{Option: "--log-format", Rule: "choose json or text"}
	}
	if _, err := diagnostic.Level(o.level); err != nil {
		return &argumentError{Option: "--log-level", Rule: "choose debug, info, warn or error"}
	}
	if o.serial != "" && !cloudhttp.ValidSerial(o.serial) {
		return &argumentError{Option: "--serial", Rule: cloudhttp.SerialRule}
	}
	if o.model != "" && !cloudhttp.ValidModel(o.model) {
		return &argumentError{Option: "--model", Rule: cloudhttp.ModelRule}
	}
	return nil
}
