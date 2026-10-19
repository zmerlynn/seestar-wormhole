package main

import (
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
	return &startupLogging{output: output, logger: diagnostic.Logger(o.format, output, level)}
}

// finish runs after owned resources close and before the final failure record.
func (s *startupLogging) finish(result error) error {
	var detail *argumentError
	loss := s.output.resolve(!errors.As(result, &detail))
	s.output.report(true)
	return loss
}
