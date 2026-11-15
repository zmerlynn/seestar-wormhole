package cloudhttp

import (
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// stateError names the state directory and keeps the OS error's text, after
// the caller has classified it.
func stateError(dir, phase, reason string, err error) error {
	return diagnostic.Wrap(phase, reason, errors.Join(cloud.ErrConfig, &diagnostic.OptionError{
		Option: "--state-dir",
		Rule:   "use a private writable state directory and installation identity file",
		Value:  dir,
		Err:    err,
	}))
}

// randomSource creates an installation UUID without accessing the filesystem.
func randomSource() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", stateError("", "state_identity", diagnostic.Class(err), err)
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}
