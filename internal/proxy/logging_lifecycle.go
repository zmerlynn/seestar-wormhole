package proxy

import (
	"io"
	"log/slog"
	"net"
	"time"
)

func localPort(addr net.Addr) int {
	if a, ok := addr.(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// clientAddress is the client's address as it is, or "" when there is none.
func clientAddress(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func clientLog(logger *slog.Logger, local net.Conn, port uint16) *slog.Logger {
	if logger == nil {
		return nil
	}
	return logger.With("port", port, "local_port", localPort(local.LocalAddr()), "client", clientAddress(local.RemoteAddr()))
}

type bridgeResult struct {
	localToDevice, deviceToLocal int64
	reason                       string
}

// countingWriter counts successful forwarded bytes, including partial writes.
// Each copy worker owns its own counter.
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

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
