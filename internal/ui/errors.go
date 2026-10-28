package ui

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// ActionError carries only a fixed category and a field name, never dependency text.
type ActionError struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
}

func (e *ActionError) Error() string { return "UI action failed: " + e.Code }
func actionError(err error) *ActionError {
	var e *ActionError
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, errStorage) {
		return &ActionError{Code: "storage"}
	}
	_, reason := diagnostic.Fields(err, "")
	if reason == "unauthorized" || reason == "reauthentication_required" || errors.Is(err, cloud.ErrConfig) {
		return &ActionError{Code: "signin", Field: "password"}
	}
	if reason == "device_selection_required" {
		return &ActionError{Code: "selection", Field: "serial"}
	}
	return &ActionError{Code: "connection"}
}
