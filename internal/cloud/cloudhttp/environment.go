package cloudhttp

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

const (
	emailRule    = "use a valid UTF-8 email of at most 4096 bytes matching the supported account format"
	passwordRule = "use a valid UTF-8 password of 8..32 UTF-16 units"
	// SerialRule is the fixed command serial-validation rule.
	SerialRule = "use a device serial of 1-64 printable ASCII bytes (0x21-0x7E; no spaces)"
	// ModelRule is the fixed command model-validation rule.
	ModelRule = "use a nonblank device model of at most 4096 bytes without NUL, CR or LF"
)

func environmentInputError(setting, rule string) error {
	return diagnostic.Wrap("config", "invalid_configuration", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: setting, Rule: rule}))
}

// ValidSerial accepts 1-64 printable ASCII bytes, excluding space.
func ValidSerial(serial string) bool {
	if len(serial) == 0 || len(serial) > 64 {
		return false
	}
	for i := range len(serial) {
		if serial[i] < 0x21 || serial[i] > 0x7e {
			return false
		}
	}
	return true
}

// ValidModel applies the same opaque model bounds as device detail requests.
func ValidModel(model string) bool { return validInput(model) }
