package proxy

import (
	"io"
	"log/slog"
	"net"
	"time"
)

// Only numeric ports and fixed address classes may cross the logging boundary.
func localPort(addr net.Addr) int {
	if a, ok := addr.(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}
func clientClass(addr net.Addr) string {
	a, ok := addr.(*net.TCPAddr)
	if !ok {
		return "other"
	}
	if a.IP.IsLoopback() {
		return "loopback"
	}
	if a.IP.IsPrivate() || a.IP.IsLinkLocalUnicast() {
		return "LAN"
	}
	return "other"
}

func clientLog(logger *slog.Logger, local net.Conn, port uint16) *slog.Logger {
	if logger == nil {
		return nil
	}
	return logger.With("port", port, "local_port", localPort(local.LocalAddr()), "client_class", clientClass(local.RemoteAddr()))
}

type bridgeResult struct {
	localToDevice, deviceToLocal int64
	reason                       string
}

// countingWriter counts successful forwarded bytes, including partial writes
// and rewritten control lines. Each copy worker owns its own counter.
type countingWriter struct {
	io.Writer
	bytes int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.bytes += int64(n)
	return n, err
}

func logChannelClose(logger *slog.Logger, start time.Time, result bridgeResult) {
	if logger != nil {
		logger.Info("channel_close", "duration_ms", milliseconds(time.Since(start)), "local_to_device_bytes", result.localToDevice, "device_to_local_bytes", result.deviceToLocal, "reason", result.reason)
	}
}
