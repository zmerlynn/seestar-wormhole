//go:build gui && (darwin || linux)

package ui

import (
	"context"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/opener"
)

func openBrowser(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return opener.Command(ctx, url).Run()
}
