package main

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"log/slog"
)

// startupLogging holds dependency output until configuration is accepted.
type startupLogging struct {
	output *startupLogOutput
	logger *slog.Logger
}

func newStartupLogging(o options, logs io.Writer) *startupLogging {
	level, _ := diagnostic.Level(o.level)
	output := &startupLogOutput{output: logs}
	return &startupLogging{output: output, logger: slog.New(catalogLogHandler{diagnostic.Logger(o.format, output, level).Handler(), diagnostic.Logger(o.format, logs, level).Handler()})}
}

// finish runs after owned resources close and before the final failure record.
func (s *startupLogging) finish(result error) error {
	var detail *argumentError
	loss := s.output.resolve(!errors.As(result, &detail))
	s.output.report(true)
	return loss
}

// The account list is requested evidence even when catalog selection refuses
// startup. Route only this fixed record around the pending dependency buffer.
type catalogLogHandler struct {
	slog.Handler
	catalog slog.Handler
}

func (h catalogLogHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "device_catalog" {
		return h.catalog.Handle(ctx, r)
	}
	return h.Handler.Handle(ctx, r)
}
func (h catalogLogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return catalogLogHandler{h.Handler.WithAttrs(a), h.catalog.WithAttrs(a)}
}
func (h catalogLogHandler) WithGroup(g string) slog.Handler {
	return catalogLogHandler{h.Handler.WithGroup(g), h.catalog.WithGroup(g)}
}
