//go:build !unix && !windows

package proxy

import "net"

// The product's Linux and Darwin TCP edges use SO_ERROR. Other connection
// implementations can expose terminal state explicitly; otherwise Read owns it.
func admissionSocketErrors(conn net.Conn) (socketErrorSource, error) {
	source, _ := conn.(socketErrorSource)
	return source, nil
}
