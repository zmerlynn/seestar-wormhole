package proxy

import (
	"context"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Dialer is the internal stream interface used by local proxy listeners.
type Dialer interface {
	DialPort(context.Context, uint16) (net.Conn, error)
}

// Forward binds a local listener to a device port. Serve takes listener ownership.
type Forward struct {
	Listener net.Listener
	Port     uint16
}

// Serve forwards clients using at most maxClients preread workers, socket error
// watchers during admission, and pairs of
// copy workers. It closes listeners and both sides of every client on
// cancellation, then joins.
// The dialer must honor cancellation; Session does. Logger must be nonblocking.
func Serve(parentCtx context.Context, dialer Dialer, forwards []Forward, maxClients int, logger *slog.Logger) error {
	if dialer == nil || maxClients < 1 || maxClients > 128 || len(forwards) == 0 || len(forwards) > 128 {
		return errors.New("proxy: invalid listener configuration")
	}
	for _, f := range forwards {
		if f.Listener == nil || f.Port == 0 {
			return errors.New("proxy: invalid forwarding port")
		}
	}
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	var mu sync.Mutex // protects socket membership only; never held over I/O
	active := make(map[net.Conn]struct{})
	add := func(c net.Conn) bool {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return false
		}
		active[c] = struct{}{}
		return true
	}
	remove := func(c net.Conn) { mu.Lock(); delete(active, c); mu.Unlock() }
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		for _, f := range forwards {
			f.Listener.Close()
		}
		mu.Lock()
		sockets := make([]net.Conn, 0, len(active))
		for c := range active {
			sockets = append(sockets, c)
		}
		mu.Unlock()
		for _, c := range sockets {
			c.Close()
		}
	}()
	slots := make(chan struct{}, maxClients)
	var accepts, clients sync.WaitGroup
	var errMu sync.Mutex
	var serveErr error
	for _, f := range forwards {
		accepts.Go(func() {
			for {
				local, err := f.Listener.Accept()
				if err != nil {
					if ctx.Err() == nil {
						errMu.Lock()
						serveErr = diagnostic.Wrap("listener_accept", "", err)
						errMu.Unlock()
						if logger != nil {
							logger.Warn("listener_accept_failed", "reason", diagnostic.Class(err), "port", f.Port, "local_port", localPort(f.Listener.Addr()))
						}
						cancel()
					}
					return
				}
				acceptLogger := clientLog(logger, local, f.Port)
				select {
				case slots <- struct{}{}:
				default:
					local.Close()
					if acceptLogger != nil {
						acceptLogger.Warn("client_rejected", "reason", "capacity")
					}
					continue
				}
				if !add(local) {
					if acceptLogger != nil {
						acceptLogger.Info("client_canceled", "reason", "canceled")
					}
					local.Close()
					<-slots
					return
				}
				clients.Go(func() {
					admission, cancelAdmission := context.WithCancel(ctx)
					defer cancelAdmission()
					reader := preread(local, cancelAdmission)
					defer func() { reader.close(); remove(local); <-slots }()
					remote, err := dialer.DialPort(admission, f.Port)
					reader.stopWatching()
					if err != nil {
						// The real session logs availability at lifecycle transitions.
						// Reconnecting local clients must not repeat that diagnosis.
						var unavailable *availabilityError
						if acceptLogger != nil && !errors.As(err, &unavailable) {
							_, reason := diagnostic.Fields(err, "channel")
							if failure := reader.failure(); failure != nil && ctx.Err() == nil {
								reason = diagnostic.Class(failure)
							}
							if errors.Is(err, context.Canceled) {
								acceptLogger.Info("client_canceled", "reason", reason)
							} else {
								acceptLogger.Warn("client_rejected", "reason", reason)
							}
						}
						return
					}
					if admission.Err() != nil || !add(remote) {
						remote.Close()
						if acceptLogger != nil {
							reason := "canceled"
							if failure := reader.failure(); failure != nil && ctx.Err() == nil {
								reason = diagnostic.Class(failure)
							}
							acceptLogger.Info("client_canceled", "reason", reason)
						}
						return
					}
					defer func() { remote.Close(); remove(remote) }()
					// Only command tables that can dispatch site setters are inspected:
					// imager ports 4700–4702 and guider port 4400.
					// Separate image/file/updater tables must retain raw byte copying.
					// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#framing-per-port-family
					// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/control/session-handshake.md#endpoints
					// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/guider/index.md#endpoints
					inspectSite := false
					switch f.Port {
					case 4400, 4700, 4701, 4702:
						inspectSite = true
					}
					clientLogger := acceptLogger
					if clientLogger != nil {
						channel := -1 // Alternate Dialer implementations may not expose a tunnel channel.
						if c, ok := remote.(*stream); ok {
							channel = int(c.channel)
						}
						clientLogger = clientLogger.With("channel", channel)
						clientLogger.Info("channel_open")
					}
					started := time.Now()
					result := bridgeReader(local, remote, reader, inspectSite, clientLogger)
					// A sibling session can close its streams before cancellation
					// reaches our derived context. Check the caller's context too.
					if parentCtx.Err() != nil || ctx.Err() != nil {
						result.reason = "canceled"
					}
					logChannelClose(clientLogger, started, result)
				})
			}
		})
	}
	accepts.Wait()
	cancel()
	<-stopped
	clients.Wait()
	return serveErr
}
func bridge(a, b net.Conn, inspectSite bool, logger *slog.Logger) {
	bridgeReader(a, b, a, inspectSite, logger)
}

func bridgeReader(a, b net.Conn, localReader io.Reader, inspectSite bool, logger *slog.Logger) bridgeResult {
	var stopOnce sync.Once
	result := bridgeResult{reason: "ended"}
	toLocal, toDevice := &countingWriter{Writer: localSocketWriter{a}}, &countingWriter{Writer: b}
	localWriter := &siteClientWriter{writer: toLocal}
	reportFailure := func(err error, direction string) {
		var local *localClosedError
		if logger != nil && errors.As(err, &local) {
			logger.Info("channel_closed", "layer", "bridge", "direction", direction, "reason", "local_closed")
			return
		}
		var channel *channelFailure
		var terminal *diagnostic.Terminal
		if logger == nil || !abnormalChannel(err) || errors.As(err, &channel) || errors.As(err, &terminal) {
			return
		}
		logger.Warn("channel_closed", "layer", "bridge", "direction", direction, "reason", diagnostic.Class(err))
	}
	stop := func(err error, direction string) {
		stopOnce.Do(func() {
			if err != nil {
				result.reason = channelCause(err)
				var terminal *diagnostic.Terminal
				if errors.As(err, &terminal) {
					_, result.reason = diagnostic.Fields(err, "channel")
				}
			}
			reportFailure(err, direction)
			a.Close()
			b.Close()
		})
	}
	copyHalf := func(dst net.Conn, writer *countingWriter, src io.Reader, inspect bool, direction string) {
		var err error
		if inspect && inspectSite {
			err = copySite(writer, src, logger, localWriter)
		} else if inspectSite && !inspect {
			err = localWriter.copyDevice(src)
		} else {
			_, err = io.CopyBuffer(writer, struct{ io.Reader }{src}, make([]byte, 32<<10))
		}
		if err != nil {
			stop(err, direction)
			return
		}
		half, ok := dst.(interface{ CloseWrite() error })
		if !ok {
			stop(nil, direction)
			return
		}
		// copyDevice has retired local replies under this lock. Serialize FIN
		// with the writer while preserving the independent upload direction.
		if !inspect && inspectSite {
			localWriter.mu.Lock()
			defer localWriter.mu.Unlock()
		}
		if err := half.CloseWrite(); err != nil {
			if !inspect {
				err = localWriteError(err)
			}
			stop(err, direction)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); copyHalf(a, toLocal, b, false, "device_to_local") }()
	copyHalf(b, toDevice, localReader, true, "local_to_device")
	<-done
	result.localToDevice, result.deviceToLocal = toDevice.bytes, toLocal.bytes
	return result
}
