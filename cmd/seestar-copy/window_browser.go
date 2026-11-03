//go:build !gui

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/zmerlynn/seestar-copy/internal/app"
)

// present shows the app without a window, for tests and development: it
// prints the page's URL and serves until Ctrl+C. It has no dialogs.
func present(out io.Writer) (func(context.Context, string, func()) error, *app.Native) {
	return func(ctx context.Context, url string, _ func()) error {
		if _, err := fmt.Fprintln(out, url); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}, nil
}
