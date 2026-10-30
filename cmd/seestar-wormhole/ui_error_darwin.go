//go:build gui && !guitest

package main

import "os/exec"

func showUIStartupError(err error) {
	// Pass text as arguments; it is never evaluated as AppleScript source.
	_ = exec.Command("/usr/bin/osascript", "-e", `on run argv
 display alert (item 1 of argv) message (item 2 of argv) as critical buttons {"OK"} default button "OK"
end run`, "--", uiStartupErrorTitle, err.Error()).Run()
}
