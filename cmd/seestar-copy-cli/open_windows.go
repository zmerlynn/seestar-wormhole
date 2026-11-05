package main

import (
	"log/slog"

	"github.com/zmerlynn/seestar-wormhole/internal/opener"
)

// openURL hands url to the shell, which opens it in the default browser.
// It returns at once; a failure is logged as a warning.
func openURL(url string, log *slog.Logger) {
	go func() {
		if err := opener.ShellExecute(url); err != nil {
			warnOpen(log, err)
		}
	}()
}
