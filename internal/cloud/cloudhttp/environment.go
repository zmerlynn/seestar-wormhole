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
	SerialRule = "use a nonblank device serial of at most 4096 bytes without NUL, CR or LF"
	// ModelRule is the fixed command model-validation rule.
	ModelRule = "use a nonblank device model of at most 4096 bytes without NUL, CR or LF"
)

func environmentInputError(setting, rule string) error {
	return diagnostic.Wrap("config", "invalid_configuration", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: setting, Rule: rule}))
}

// ValidSerial applies the same opaque serial bounds as device detail requests.
func ValidSerial(serial string) bool { return validInput(serial) }

// ValidModel applies the same opaque model bounds as device detail requests.
func ValidModel(model string) bool { return validInput(model) }
