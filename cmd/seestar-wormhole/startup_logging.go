package main

import (
	"errors"
	"io"
	"log/slog"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

// startupLogging holds dependency and capture output until configuration is
// accepted. Both commands discard it on a typed configuration refusal.
type startupLogging struct {
	output                    *startupLogOutput
	logger                    *slog.Logger
	handler                   *startupKeylogHandler
	keys                      *keylog.Writer
	keylogFailed, keylogReady bool
}

func newStartupLogging(o options, logs io.Writer) *startupLogging {
	level, _ := diagnostic.Level(o.level)
	output := &startupLogOutput{output: logs}
	logger := diagnostic.Logger(o.format, output, level)
	return &startupLogging{output: output, logger: logger}
}

func (s *startupLogging) openKeys(path string, open func(string, *slog.Logger) (*keylog.Writer, error)) *keylog.Writer {
	if path == "" {
		return nil
	}
	s.handler = &startupKeylogHandler{Handler: s.logger.With("option", "--keylog", "rule", "use a writable private key file").Handler()}
	var err error
	s.keys, err = open(path, slog.New(s.handler))
	s.keylogFailed = err != nil
	return s.keys
}

func (s *startupLogging) warnKeylogFailure() {
	s.logger.Warn("keylog_failed", "count", 1, "option", "--keylog", "rule", "use a new writable private key file")
}

func (s *startupLogging) acceptKeys() {
	s.keylogReady = true
	if s.handler != nil {
		s.handler.resolve(true)
	}
	if s.keylogFailed {
		s.warnKeylogFailure()
	} else if s.keys != nil {
		s.logger.Info("key_logging_enabled", "warning", "capture decryption secrets are being saved; delete after analysis")
	}
}

// finish runs after owned resources close and before the final failure record.
// Closing the key writer first includes its one-shot shutdown warning in the
// configuration decision, including when it could not write any key material.
func (s *startupLogging) finish(result error) error {
	var loss error
	if s.keys != nil {
		s.keys.Close()
	}
	var detail *argumentError
	accepted := !errors.As(result, &detail)
	if s.handler != nil {
		loss = errors.Join(loss, s.handler.resolve(accepted))
		s.handler.mu.Lock()
		lost := s.handler.lost
		s.handler.mu.Unlock()
		if lost > 0 {
			s.output.mu.Lock()
			s.output.lost += lost
			s.output.err = io.ErrShortWrite
			s.output.mu.Unlock()
		}
	}
	if !s.keylogReady && accepted && s.keylogFailed {
		s.warnKeylogFailure()
	}
	loss = errors.Join(loss, s.output.resolve(accepted))
	s.output.report(true)
	return loss
}

func (s *startupLogging) dropped() uint64 {
	s.output.mu.Lock()
	n := s.output.lost
	s.output.mu.Unlock()
	if s.keys != nil {
		n += s.keys.Dropped()
	}
	return n
}
