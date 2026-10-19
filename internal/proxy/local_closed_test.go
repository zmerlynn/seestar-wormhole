package proxy

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"syscall"
	"testing"
)

// Send one response successfully, then fail the next event write. The other
// direction half-closes, so its EOF cannot hide the downstream write outcome.
type eventWriteFailure struct {
	calls   int
	failure error
}

func (w *eventWriteFailure) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 1 {
		return len(p), nil
	}
	return 0, w.failure
}

type eventReader struct{ calls int }

func (r *eventReader) Read(p []byte) (int, error) {
	r.calls++
	if r.calls > 2 {
		return 0, io.EOF
	}
	if r.calls == 1 {
		return copy(p, "answer\r\n"), nil
	}
	return copy(p, "event\r\n"), nil
}

func TestBridgeLocalPeerClosed(t *testing.T) {
	for _, inspect := range []bool{false, true} {
		for _, failure := range []error{syscall.EPIPE, syscall.ECONNRESET, net.ErrClosed} {
			t.Run(fmt.Sprintf("inspect=%t/%v", inspect, failure), func(t *testing.T) {
				var logs bytes.Buffer
				writer := &eventWriteFailure{failure: fmt.Errorf("PRIVATE_LOCAL: %w", failure)}
				local := &siteFragmentConn{Reader: bytes.NewReader(nil), Writer: writer}
				device := &siteFragmentConn{Reader: &eventReader{}, Writer: io.Discard}
				result := bridgeReader(local, device, local, inspect, slog.New(slog.NewJSONHandler(&logs, nil)))
				if writer.calls != 2 || result.deviceToLocal != int64(len("answer\r\n")) || result.reason != "local_closed" {
					t.Errorf("writes=%d result=%+v", writer.calls, result)
				}
				records := loggingRecords(t, &logs)
				if len(records) != 1 || records[0]["msg"] != "channel_closed" || records[0]["level"] != "INFO" || records[0]["reason"] != "local_closed" {
					t.Fatalf("want one INFO local_closed, got %s", &logs)
				}
			})
		}
	}
}

func TestBridgeDeviceFailuresWarn(t *testing.T) {
	for _, inspect := range []bool{false, true} {
		for _, operation := range []string{"read", "write"} {
			for _, failure := range []error{syscall.EPIPE, syscall.ECONNRESET, net.ErrClosed} {
				t.Run(fmt.Sprintf("inspect=%t/%s/%v", inspect, operation, failure), func(t *testing.T) {
					var logs bytes.Buffer
					local := &siteFragmentConn{Reader: bytes.NewReader(nil), Writer: io.Discard}
					device := &siteFragmentConn{Reader: bytes.NewReader(nil), Writer: io.Discard}
					if operation == "read" {
						device.Reader = &siteErrorReader{err: failure}
					} else {
						local.Reader = bytes.NewBufferString("{\"method\":\"get_view_state\"}\r\n")
						device.Writer = &siteErrorWriter{err: failure}
					}
					result := bridgeReader(local, device, local, inspect, slog.New(slog.NewJSONHandler(&logs, nil)))
					if result.reason == "local_closed" {
						t.Errorf("device error labeled local close: %+v", result)
					}
					records := loggingRecords(t, &logs)
					if failure == net.ErrClosed {
						if len(records) != 0 || result.reason != "local_close" {
							t.Fatalf("want quiet closed-handle teardown, got result=%+v logs=%s", result, &logs)
						}
						return
					}
					wantDirection := "device_to_local"
					if operation == "write" {
						wantDirection = "local_to_device"
					}
					if len(records) != 1 || records[0]["level"] != "WARN" || records[0]["direction"] != wantDirection {
						t.Fatalf("want device WARN in %s, got %s", wantDirection, &logs)
					}
				})
			}
		}
	}
}
