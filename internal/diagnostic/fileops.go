package diagnostic

import "errors"

// The sources of a file-tool outcome: the tool an operation belongs to.
// Connection names the connection lines of the app's trail, and Find and
// Scan only the CLI's find and scan commands.
const (
	SourceConnection = "connection"
	SourceCopy       = "copy"
	SourceBrowse     = "browse"
	SourceDelete     = "delete"
	SourceFind       = "find"
	SourceScan       = "scan"
)

// The operations of a file tool. List and Run are the CLI's only: find's
// listing, and a command's initialization.
const (
	OpStart          = "start"
	OpOpen           = "open"
	OpOtherTelescope = "other_telescope"
	OpPass           = "pass"
	OpPreview        = "preview"
	OpTest           = "test"
	OpSaveSettings   = "save_settings"
	OpClearHistory   = "clear_history"
	OpChooseFolder   = "choose_folder"
	OpConnect        = "connect"
	OpRecords        = "records"
	OpRead           = "read"
	OpSaveFile       = "save_file"
	OpDelete         = "delete"
	OpSaveOptions    = "save_options"
	OpList           = "list"
	OpRun            = "run"
)

// Code is the fixed code of an outcome of op in source:
// <source>_<op>_failed, or <source>_<op>_recovered.
func Code(source, op string, failed bool) string {
	if failed {
		return source + "_" + op + "_failed"
	}
	return source + "_" + op + "_recovered"
}

// phased marks the phase an error happened in without changing its text,
// so a log's error key keeps the raw message.
type phased struct {
	phase string
	err   error
}

func (e *phased) Error() string { return e.err.Error() }
func (e *phased) Unwrap() error { return e.err }

// At marks err as a failure in phase, keeping its text. A nil err stays nil.
func At(phase string, err error) error {
	if err == nil {
		return nil
	}
	return &phased{phase: phase, err: err}
}

// OpFields returns the classified phase and reason of a failure of op: the
// phase that [Wrap] or [At] attached, or op. A nil err is reason none.
func OpFields(op string, err error) (phase, reason string) {
	if p, ok := errors.AsType[*phased](err); ok {
		op = p.phase
	}
	return Fields(err, op)
}

// OpKeys returns the source, op, phase and reason keys of a failure of op in
// source, for a log record. Like [Fields], it never formats err.
func OpKeys(source, op string, err error) []any {
	phase, reason := OpFields(op, err)
	return []any{"source", source, "op", op, "phase", phase, "reason", reason}
}
