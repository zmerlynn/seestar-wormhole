//go:build windows

package cloud

import (
	"github.com/zmerlynn/seestar-wormhole/internal/oserror"
	"syscall"
)

func nativeRefusal(err error) bool { return oserror.Is(err, syscall.ECONNREFUSED) }
