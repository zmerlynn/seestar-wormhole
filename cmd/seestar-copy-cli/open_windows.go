package main

import (
	"log/slog"

	"golang.org/x/sys/windows"
)

// openURL hands url to the shell, which opens it in the default browser.
// It returns at once; a failure is logged as a warning.
func openURL(url string, log *slog.Logger) {
	go func() {
		if err := shellExecute(url); err != nil {
			warnOpen(log, err)
		}
	}()
}

func shellExecute(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
