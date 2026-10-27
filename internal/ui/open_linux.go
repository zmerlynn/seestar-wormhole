package ui

import (
	"context"
	"os/exec"
	"time"
)

func openBrowser(url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "xdg-open", url).Run()
}
