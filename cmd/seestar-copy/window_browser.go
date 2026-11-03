//go:build !gui

package main

import (
	"context"
	"fmt"
	"io"
)

// present shows the app without a window, for tests and development: it
// prints the page's URL and serves until Ctrl+C.
func present(out io.Writer) func(context.Context, string) error {
	return func(ctx context.Context, url string) error {
		if _, err := fmt.Fprintln(out, url); err != nil {
			return err
		}
		<-ctx.Done()
		return nil
	}
}
