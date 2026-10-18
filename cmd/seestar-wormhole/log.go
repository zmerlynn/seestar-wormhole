package main

import (
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"log/slog"
)

func newLogger(format string, w io.Writer) *slog.Logger {
	return diagnostic.Logger(format, w, slog.LevelInfo)
}
