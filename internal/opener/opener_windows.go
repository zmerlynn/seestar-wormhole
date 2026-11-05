package opener

import "golang.org/x/sys/windows"

// ShellExecute hands target to the shell's open verb, which starts the
// default browser. It returns once the shell has taken it.
func ShellExecute(target string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
