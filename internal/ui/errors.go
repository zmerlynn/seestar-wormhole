package ui

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// ActionError carries fixed categories and, only for multiple-device selection,
// received account choices. Dependency error text never enters this projection.
type ActionError struct {
	Code    string                   `json:"code"`
	Field   string                   `json:"field,omitempty"`
	Service uint16                   `json:"service,omitempty"`
	Local   uint16                   `json:"local,omitempty"`
	Devices []cloudhttp.DeviceChoice `json:"devices,omitempty"`
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
	if errors.As(err, &validation) && errors.Is(err, cloud.ErrConfig) {
		switch validation.Field {
		case "email", "password":
			return &ActionError{Code: validation.Field, Field: validation.Field}
		}
	}
	phase, reason := diagnostic.Fields(err, "")
	if reason == "busy" || reason == "ownership_uncertain" || reason == "release_abandoned" {
		return &ActionError{Code: "busy"}
	}
	if phase == "lan_discovery" {
		return &ActionError{Code: "discovery", Field: "discovery"}
	}
	var selection *cloudhttp.SelectionError
	if errors.As(err, &selection) && selection.Multiple {
		return &ActionError{Code: "selection_multiple", Devices: selection.Choices()}
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
	case "busy":
		return "Seestar is occupied. If another remote session is open, disconnect it."
	case "email":
		return "Enter your Seestar account email."
	case "password":
		return "Enter a password or use a saved password."
	case "idle":
		return "Enter a wait time greater than zero."
	case "ports":
		return "Use ports 1–65535, with no duplicates. Leave a port empty to turn off its service."
	case "ports_empty":
		return "Enter a local port for at least one service in Advanced."
	case "bind_empty":
		return "Enter a local network address, or choose This computer only or All networks."
	case "bind":
		return "Could not listen on this address. In Advanced, choose one of this computer's network addresses."
	case "discovery_bind":
		return "For Seestar app discovery, choose All networks or enter this computer's IPv4 address."
	case "discovery":
		return "Could not start Seestar app discovery. Check the network address or stop another discovery service."
	case "http_permission":
		return "Could not open the image-download port. See the port permissions guide, then connect again."
	case "http_conflict":
		return "A local port is in use. Stop the app using it or change the port in Advanced. Seestar app image access is unavailable."
	case "port_conflict":
		return "A local port is in use. Stop the app using it or change the port in Advanced."
	case "signin":
		return "Could not sign in. Check your Seestar account email and password, then connect again."
	case "selection_multiple":
		return "Your account has several telescopes. Choose one, then Connect."
	case "selection", "serial", "model":
		return "Check this setting."
	case "release":
		return "Session release could not be confirmed. If Seestar is occupied, wait and try Connect again."
	case "storage":
		return "Settings could not be saved; they may reset next time."
	case "password_save":
		return "Could not save to keychain. You can use this password until you quit."
	case "password_remove":
		return "Password cleared from this app. Could not remove the keychain copy. Unlock your keychain and choose Retry removal."
	case "password_read":
		return "Could not read the saved password. Unlock your keychain and try again, or remove the saved password."
	case "password_missing":
		return "Saved password not found. Remove the saved password, then enter it again."
	case "keychain_timeout":
		return "Keychain has not responded. Its request may still finish; wait before trying again."
	default:
		return "Could not connect. Check your network and try again."
	}
}
