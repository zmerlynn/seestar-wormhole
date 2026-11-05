//go:build gui

package ui

import "github.com/zmerlynn/seestar-wormhole/internal/opener"

func openBrowser(url string) error {
	return opener.ShellExecute(url)
}
