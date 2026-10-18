package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"log/slog"
	"sync"
)

func writeSite(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		return io.ErrShortWrite
	}
	return err
}

// copySite retains at most MaxSiteLine bytes. Overlong frames are discarded
// through LF, never forwarding a prefix or inspecting their suffix as a request.
// reply shares frame serialization with device replies in the TCP bridge.
func copySite(dst io.Writer, src io.Reader, logger *slog.Logger, reply io.Writer) error {
	r := bufio.NewReaderSize(src, MaxSiteLine)
	var line []byte
	var overlong bool
	emit := func() error {
		var output []byte
		var event *siteEvent
		if overlong {
			output, event = rejectSite(nil, "", "frame_too_large", -32600)
		} else {
			output, event = protectSite(line)
		}
		if event != nil && logger != nil {
			if event.reason == "site_protected" {
				logger.Warn("site_protected", "method", event.method, "id", event.id, "reason", event.reason)
			} else {
				logger.Warn("site_reject", "method", event.method, "reason", event.reason)
			}
		}
		if event != nil && event.rejection != nil {
			return writeSite(reply, event.rejection)
		}
		if err := writeSite(dst, output); err != nil {
			return err
		}
		return nil
	}
	for {
		var err error
		line, overlong, err = readSiteFrame(r)
		if len(line) > 0 || overlong {
			if emitErr := emit(); emitErr != nil {
				return emitErr
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readSiteFrame bounds control frames, returning EOF fragments for local rejection.
// The proxy requires CRLF although the device accepts CR or LF independently:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/ports.md#framing-per-port-family
func readSiteFrame(r *bufio.Reader) (line []byte, overlong bool, err error) {
	for {
		part, readErr := r.ReadSlice('\n')
		terminated := len(part) > 0 && part[len(part)-1] == '\n'
		size := len(line) + len(part)
		if !overlong && (size > MaxSiteLine || size == MaxSiteLine && !terminated) {
			line = nil
			overlong = true
		}
		if !overlong {
			line = append(line, part...)
		}
		if terminated || !errors.Is(readErr, bufio.ErrBufferFull) {
			return line, overlong, readErr
		}
	}
}

// siteClientWriter serializes locally generated errors with whole downstream
// frames and retires local replies when the device stream ends. This is an edge
// write lock, not a lock protecting protocol state.
// The bridge closes the sockets on cancellation to release blocked I/O.
type siteClientWriter struct {
	mu     sync.Mutex
	writer io.Writer
	closed bool // protected by mu, including termination within a partial frame
}

func (w *siteClientWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return len(b), nil
	}
	return w.writer.Write(b)
}

// copyDevice forwards every downstream byte immediately with bounded storage. Holding
// the write lock through an overlong/fragmented response keeps local errors
// from being injected in the middle of a device frame.
func (w *siteClientWriter) copyDevice(src io.Reader) error {
	buf := make([]byte, MaxSiteLine)
	locked := false
	defer func() {
		if locked {
			w.mu.Unlock()
		}
	}()
	for {
		n, err := src.Read(buf)
		part := buf[:n]
		for len(part) > 0 {
			if !locked {
				w.mu.Lock()
				locked = true
			}
			end := bytes.IndexByte(part, '\n')
			size := len(part)
			if end >= 0 {
				size = end + 1
			}
			if writeErr := writeSite(w.writer, part[:size]); writeErr != nil {
				return writeErr
			}
			part = part[size:]
			if end >= 0 {
				w.mu.Unlock()
				locked = false
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if !locked {
					w.mu.Lock()
					locked = true
				}
				// Retire replies before releasing the final frame lock or sending
				// FIN. Real failures return immediately so the bridge can stop I/O.
				w.closed = true
				return nil
			}
			return err
		}
	}
}
