package cloud

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrReauthentication means credentials must be explicitly supplied again. Remote
// 401 never starts the legacy account authenticator.
var ErrReauthentication = errors.New("cloud: reauthentication required")

// Credentials are ephemeral. Environment values are explicit login arguments:
// unlike UI default arguments, they are not trimmed or stripped of spaces.
type Credentials struct{ email, password string }

func (Credentials) Format(s fmt.State, v rune) { io.WriteString(s, "[account credentials]") }
func (Credentials) LogValue() slog.Value       { return slog.StringValue("[account credentials]") }

// EmailCredentials validates the recovered email regex and 8..32 UTF-16-unit
// password bound without changing explicitly supplied credential values.
// Account wire behavior and explicit local validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#input-preparation
func EmailCredentials(email, password string) (Credentials, error) {
	if len(email) > 4096 || !utf8.ValidString(email) || !regexp.MustCompile(`^\w+([-+.]\w+)*@\w+([-.]\w+)*\.\w+([-.]\w+)*$`).MatchString(email) {
		return Credentials{}, validation("email", "supported_email_format", ErrConfig)
	}
	if !utf8.ValidString(password) || len(password) > 128 {
		return Credentials{}, validation("password", "8_to_32_utf16_units", ErrConfig)
	}
	units := 0
	for _, r := range password {
		units += utf16.RuneLen(r)
	}
	if units < 8 || units > 32 {
		return Credentials{}, validation("password", "8_to_32_utf16_units", ErrConfig)
	}
	return Credentials{email, password}, nil
}

// LoginBody returns secret-bearing wire bytes, never suitable for logging.
// Gson additionally escapes '=' and apostrophe; retain HTML/JS escaping too.
// Account wire behavior and explicit local validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#request
func (c Credentials) LoginBody(renewal bool) []byte {
	category := 1
	if renewal {
		category = 0
	}
	b, _ := json.Marshal(struct {
		Key      string `json:"authKey"`
		Type     string `json:"authType"`
		Category int    `json:"category"`
		Language int    `json:"language"`
		Password string `json:"password"`
	}{c.email, "email", category, 0, c.password}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	b = bytes.ReplaceAll(b, []byte("="), []byte(`\u003d`))
	return bytes.ReplaceAll(b, []byte("'"), []byte(`\u0027`))
}

// AccessToken is the complete opaque Authorization value, with no added scheme.
type AccessToken struct{ value string }

func (t AccessToken) Value() string            { return t.value }
func (AccessToken) Format(s fmt.State, v rune) { io.WriteString(s, "[account token]") }
func (AccessToken) LogValue() slog.Value       { return slog.StringValue("[account token]") }

// AccountError retains distinct HTTP and envelope codes, without guessing an
// undocumented bad-password/lockout/region mapping. Untrusted messages are
// discarded: even a server's diagnostic text can contain credentials.
type AccountError struct {
	Status, Code int
	HasCode      bool
	cause        error
}

func (e *AccountError) Error() string {
	return fmt.Sprintf("cloud: account HTTP %d: %v", e.Status, e.cause)
}
func (e *AccountError) Unwrap() error              { return e.cause }
func (e *AccountError) Format(s fmt.State, v rune) { io.WriteString(s, e.Error()) }
func (e *AccountError) LogValue() slog.Value       { return slog.StringValue(e.Error()) }

func accountEnvelope(status int, b []byte) (response, *AccountError) {
	// Account codes are diagnostic, unlike remote allocation/stop outcomes.
	// A supplied code must still decode as an integer through JSON v2.
	var r response
	var err error
	if len(b) > MaxBody {
		err = invalid("body", "body_limit")
	} else {
		err = decodeJSON(b, &r)
	}
	e := &AccountError{Status: status, cause: err}
	if r.Code != nil {
		e.Code, e.HasCode = *r.Code, true
	}
	return r, e
}

// ParseLogin accepts HTTP 200 for interactive login and any 2xx for credential
// replay. Only the opaque token and diagnostic envelope code are consumed;
// token type, expiry and refresh metadata are ignored.
// Account wire behavior and explicit local validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#response
func ParseLogin(status int, b []byte, renewal bool) (AccessToken, error) {
	o, e := accountEnvelope(status, b)
	if status != 200 && !(renewal && status >= 200 && status < 300) {
		e.cause = validation("http_status", "accepted_status", ErrRejected)
		return AccessToken{}, e
	}
	if e.cause != nil {
		return AccessToken{}, e
	}
	var d struct {
		Token string `json:"accessToken"`
	}
	if err := decodeJSON(o.Data, &d); err != nil {
		e.cause = err
		return AccessToken{}, e
	}
	token := d.Token
	if len(token) > 4096 || strings.TrimSpace(token) == "" {
		e.cause = invalid("data.accessToken", "nonblank_string_up_to_4096_bytes")
		return AccessToken{}, e
	}
	for _, r := range token {
		if r < 32 || r == 127 {
			e.cause = invalid("data.accessToken", "no_control_characters")
			return AccessToken{}, e
		}
	}
	return AccessToken{token}, nil
}

// ParseProfile accepts HTTP 200/201 and a bodyless 204. A supplied diagnostic
// envelope code is type-checked but has no success predicate. Profile IDs and
// personal fields are not retained; a present profile must supply its ID. A profile
// failure does not undo an already installed token (explicit replacement policy).
// Account wire behavior and explicit local validation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/login.md#installation-and-profile-fetch
func ParseProfile(status int, b []byte) error {
	if status == 204 && len(b) == 0 {
		return nil
	}
	o, e := accountEnvelope(status, b)
	if status != 200 && status != 201 && status != 204 {
		e.cause = validation("http_status", "accepted_status", ErrRejected)
		if status == 401 {
			e.cause = validation("http_status", "unauthorized", ErrUnauthorized)
		}
		return e
	}
	if e.cause != nil {
		return e
	}
	if len(o.Data) != 0 {
		var profile *struct {
			ID *string `json:"id"`
		}
		if err := decodeJSON(o.Data, &profile); err != nil {
			e.cause = err
			return e
		}
		if profile != nil && profile.ID == nil {
			e.cause = invalid("data.id", "string")
			return e
		}
	}
	return nil
}
