// Package engine runs seestar-copy's work on the telescope's share: the
// selection of what part of the share to work on, the sync passes and the
// delete run. Commands call it in-process with checked options; flag
// parsing, usage errors and exit codes stay with the command.
//
// The engine never calls readonly.Enable or readonly.Reset. A caller that
// wants a dry run guarded turns the guard on before calling it.
package engine
