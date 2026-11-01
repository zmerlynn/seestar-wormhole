package cloud

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// MaxBody is a local policy limit, not a vendor-negotiated size.
const MaxBody = 64 << 10

var (
	ErrInvalid        = errors.New("cloud: invalid response")
	ErrLicenseMissing = errors.New("cloud: allocation carried no SDK license key")
	ErrBusy           = errors.New("cloud: device in use or service busy")
	ErrOffline        = errors.New("cloud: device offline")
	ErrRejected       = errors.New("cloud: request rejected")
	ErrUnauthorized   = errors.New("cloud: token expired or unauthorized")
	ErrTransport      = errors.New("cloud: HTTP exchange failed")
	ErrConfig         = errors.New("cloud: invalid configuration")
)

// Identifier is an opaque cloud stop identifier. Formatting always redacts it.
// Value is only for the HTTPS stop adapter, never for logs.
type Identifier struct{ value string }

func (i Identifier) Value() string               { return i.value }
func (Identifier) LogValue() slog.Value          { return slog.StringValue("[cloud identifier]") }
func (Identifier) Format(s fmt.State, verb rune) { io.WriteString(s, "[cloud identifier]") }

// Allocation owns immutable connection material. Accessors explicitly cross the
// secret boundary; do not log their results. License returns a fresh byte slice.
type Allocation struct {
	id                               Identifier
	uid, license, username, password string
}

func (a Allocation) ConnectionID() string        { return a.id.value }
func (a Allocation) ID() Identifier              { return a.id }
func (a Allocation) UID() string                 { return a.uid }
func (a Allocation) License() []byte             { return []byte(a.license) }
func (a Allocation) Username() string            { return a.username }
func (a Allocation) Password() string            { return a.password }
func (Allocation) LogValue() slog.Value          { return slog.StringValue("[cloud allocation]") }
func (Allocation) Format(s fmt.State, verb rune) { io.WriteString(s, "[cloud allocation]") }

// Detail is the minimal typed device-selection result.
type Detail struct {
	serial, model, name string
	status              int
}

func (d Detail) Serial() string { return d.serial }
func (d Detail) Model() string  { return d.model }

// Name returns the cloud SSID.
func (d Detail) Name() string                { return d.name }
func (d Detail) Status() int                 { return d.status }
func (Detail) LogValue() slog.Value          { return slog.StringValue("[cloud device]") }
func (Detail) Format(s fmt.State, verb rune) { io.WriteString(s, "[cloud device]") }

func cloudCode(r response) error {
	// Absent/null diagnostic codes are allowed on successful HTTP responses.
	if r.Code == nil {
		return nil
	}
	switch *r.Code {
	case 300403, 300404:
		return validation("code", "vendor_rejection", ErrBusy)
	case 300406:
		return validation("code", "vendor_rejection", ErrOffline)
	case 300405:
		return validation("code", "vendor_rejection", ErrRejected)
	}
	return nil
}

func validString(s string) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsRune(s, 0)
}

// ParseOnline returns a cleanup identifier from a complete JSON response, even
// when other consumed fields or the cloud code prevent allocation. The caller
// must stop it unless ownership transfers to a lease with deferred cleanup.
// Local policy refuses null/blank consumed connection strings rather than using
// Gson defaults.
// Consumed response fields and cleanup boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link
func ParseOnline(b []byte) (Allocation, []Identifier, error) {
	r, err := readResponse(b)
	var d struct {
		ID       string `json:"conId"`
		UID      string `json:"deviceUid"`
		License  string `json:"sdkLicenseKey"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// V2 stops at the first type error. Extract cleanup independently so an
	// earlier invalid password/UID cannot hide the identifier in valid JSON.
	var cleanup struct {
		ID string `json:"conId"`
	}
	_ = decodeJSON(r.Data, &cleanup)
	dataErr := decodeJSON(r.Data, &d)
	var ids []Identifier
	if validString(cleanup.ID) {
		ids = []Identifier{{cleanup.ID}}
	}
	if err != nil {
		return Allocation{}, ids, err
	}
	if err := cloudCode(r); err != nil {
		return Allocation{}, ids, err
	}
	if dataErr != nil {
		return Allocation{}, ids, dataErr
	}
	for _, field := range []struct{ name, value string }{
		{"conId", d.ID}, {"deviceUid", d.UID}, {"sdkLicenseKey", d.License},
		{"username", d.Username}, {"password", d.Password},
	} {
		if !validString(field.value) {
			if field.name == "sdkLicenseKey" && strings.TrimSpace(field.value) == "" {
				return Allocation{}, ids, validation("data.sdkLicenseKey", "license_missing", ErrLicenseMissing)
			}
			return Allocation{}, ids, invalid("data."+field.name, "nonblank_string_without_nul")
		}
	}
	return Allocation{Identifier{d.ID}, d.UID, d.License, d.Username, d.Password}, ids, nil
}

// deviceIdentity validates the prerequisites shared by list and detail.
// Catalog selection consumes only serial/model after validation.
type deviceIdentity struct {
	Serial string  `json:"deviceSn"`
	Model  string  `json:"deviceModel"`
	Status *int    `json:"deviceStatus"`
	SSID   *string `json:"ssid"`
}

func (d deviceIdentity) detail() (Detail, error) {
	// Local policy refuses missing/null consumed prerequisites rather
	// than Gson's default 0 or empty string. Model is selection authority.
	if d.Status == nil {
		return Detail{}, invalid("data.deviceStatus", "integer")
	}
	if d.SSID == nil {
		return Detail{}, invalid("data.ssid", "string")
	}
	if !ValidSerial(d.Serial) {
		return Detail{}, invalid("data.deviceSn", "serial_ascii_1_64")
	}
	if !validString(d.Model) {
		return Detail{}, invalid("data.deviceModel", "nonblank_string_without_nul")
	}
	return Detail{serial: d.Serial, model: d.Model}, nil
}

// ParseDetail requires identity and status; unknown status values are retained.
// Refusals return decoded detail when a valid record is present so callers can
// correlate identity before applying availability to the requested device.
// Consumed response fields and cleanup boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#device-selection
func ParseDetail(b []byte) (Detail, error) {
	r, err := readResponse(b)
	if err != nil {
		return Detail{}, err
	}
	// Error envelopes need no record. If one is supplied, retain a valid
	// identity even with a refusal so the HTTP adapter can correlate it
	// before attributing availability to the requested pair (local client policy).
	codeErr := cloudCode(r)
	// Correlation consumes identity independently of availability and LAN
	// metadata. A malformed status or SSID must not hide a different pair,
	// including when the envelope itself refuses the request.
	var pair struct {
		Serial string `json:"deviceSn"`
		Model  string `json:"deviceModel"`
	}
	var identity Detail
	if decodeJSON(r.Data, &pair) == nil && ValidSerial(pair.Serial) && validString(pair.Model) {
		identity = Detail{serial: pair.Serial, model: pair.Model}
	}
	var d deviceIdentity
	if err := decodeJSON(r.Data, &d); err != nil {
		if codeErr != nil {
			return identity, codeErr
		}
		return identity, err
	}
	detail, err := d.detail()
	if err != nil {
		if codeErr != nil {
			return identity, codeErr
		}
		return identity, err
	}
	detail.status = *d.Status
	if codeErr != nil {
		return detail, codeErr
	}
	// Local policy per the 2026-10-15 presence/nullability answer: refuse
	// 4 as busy and 0 as offline before dialing; retain unknown integers.
	// The app precheck refuses only 2 and maps unknown integers to offline.
	switch detail.status {
	case 4:
		return detail, validation("data.deviceStatus", "occupied", ErrBusy)
	case 0, 2:
		return detail, validation("data.deviceStatus", "offline", ErrOffline)
	}
	return detail, nil
}

// ParseStop consumes only the envelope code; msg and data are ignored.
// Consumed response fields and cleanup boundary:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop
func ParseStop(b []byte) error {
	r, err := readResponse(b)
	if err != nil {
		return err
	}
	return cloudCode(r)
}

// ParseLANDetail additionally retains the required cloud ssid for LAN discovery.
func ParseLANDetail(b []byte) (Detail, error) {
	d, err := ParseDetail(b)
	if err != nil {
		return d, err
	}
	r, err := readResponse(b)
	if err != nil {
		return Detail{}, err
	}
	var name struct {
		Name string `json:"ssid"`
	}
	if err := decodeJSON(r.Data, &name); err != nil {
		return Detail{}, err
	}
	d.name = name.Name
	return d, nil
}
