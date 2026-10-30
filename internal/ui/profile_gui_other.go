//go:build gui && !windows

package ui

// Wails exposes no website-data-store/network-session option for WKWebView or
// WebKitGTK. These engines retain their default persistent stores.
func windowProfile(string) (string, func() error, error) {
	return "", func() error { return nil }, nil
}
