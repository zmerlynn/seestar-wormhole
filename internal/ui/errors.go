package ui

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// ActionError carries only a fixed category and a field name, never dependency text.
type ActionError struct {
	Code    string `json:"code"`
	Field   string `json:"field,omitempty"`
	Service uint16 `json:"service,omitempty"`
	Local   uint16 `json:"local,omitempty"`
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
	var validation *cloud.ValidationError
	if errors.As(err, &validation) && errors.Is(err, cloud.ErrConfig) && validation.Field == "email" {
		return &ActionError{Code: "email", Field: "email"}
	}
	phase, reason := diagnostic.Fields(err, "")
	if phase == "lan_discovery" {
		return &ActionError{Code: "discovery", Field: "discovery"}
	}
	if phase == "device_selection" {
		return &ActionError{Code: "selection", Field: "serial"}
	}
	if reason == "unauthorized" || reason == "reauthentication_required" || errors.Is(err, cloud.ErrConfig) {
		return &ActionError{Code: "signin", Field: "password"}
	}
	if reason == "device_selection_required" {
		return &ActionError{Code: "selection", Field: "serial"}
	}
	return &ActionError{Code: "connection"}
}

func errorCopy(code string) string {
	switch code {
	case "email":
		return "Enter the email address of your Seestar account."
	case "password":
		return "Enter a password or use a saved password."
	case "idle":
		return "Enter an idle time greater than zero."
	case "ports":
		return "Enter a local port from 1 to 65535, or leave it empty to turn the service off. Use each local port only once."
	case "ports_empty":
		return "Enable at least one service by entering a local port in Advanced."
	case "bind":
		return "Could not listen on this address. Choose an address assigned to this computer in Advanced."
	case "discovery_bind":
		return "Phone discovery cannot use this address. Choose All networks or a local IPv4 network address."
	case "discovery":
		return "Could not start phone discovery. Check the local network address and discovery port in Advanced."
	case "http_permission":
		return "Could not open the port needed for phone album/image access. Follow the port permissions guide, then connect again."
	case "http_conflict":
		return "A local port is already in use. Stop the other proxy or change that port in Advanced. Phone album/image access is incomplete."
	case "port_conflict":
		return "A local port is already in use. Stop the other proxy or change that port in Advanced."
	case "signin":
		return "Sign-in was rejected. Check your Seestar account email and password, then connect again."
	case "selection":
		return "Choose your telescope in Advanced. Enter its device serial and model from your Seestar account."
	case "serial":
		return "Check the device serial in Advanced."
	case "model":
		return "Check the device model in Advanced."
	case "release":
		return "Session release could not be confirmed. If Connect fails as busy, wait a minute and try again."
	case "storage":
		return "Connected. Settings could not be saved; they may reset next time."
	case "password_save":
		return "Could not save to keychain. You can connect using this password for this launch."
	case "password_remove":
		return "Password cleared from this app. Could not remove the keychain copy. Unlock your keychain and choose Retry removal."
	case "password_read":
		return "Could not read the saved password. Unlock your keychain and try again, or enter a password for this launch."
	case "password_missing":
		return "The saved password was not found. Enter it again."
	case "keychain_timeout":
		return "The keychain has not responded. Its request may still finish. You can enter a password for this launch."
	default:
		return "Could not connect. Check your network and try again."
	}
}
