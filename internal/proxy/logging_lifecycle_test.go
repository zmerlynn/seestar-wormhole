package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

type logAddressConn struct{ net.Conn }

func (logAddressConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.168.42.99"), Port: 14445}
}
func (logAddressConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.168.42.88"), Port: 56789}
}

func loggingRecords(t *testing.T, b *bytes.Buffer) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(b.Bytes()), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var r map[string]any
		if err := json.Unmarshal(line, &r, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	for _, private := range []string{"192.168.42.99", "192.168.42.88", "56789", "PRIVATE"} {
		if bytes.Contains(b.Bytes(), []byte(private)) {
			t.Fatalf("private address/error in logs: %s", b)
		}
	}
	return records
}

func TestListenerLifecycleLogs(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "")
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s, _, _ := workloadSession(t, ctx, func(*simulatedWire) {}, Config{Logger: logger})
				a, client := net.Pipe()
				defer client.Close()
				l := &e2eSingleListener{conn: logAddressConn{a}, done: make(chan struct{})}
				done := make(chan error, 1)
				go func() { done <- Serve(ctx, s, []Forward{{Listener: l, Port: 4700}}, 1, logger) }()
				request := []byte(rpcLine(42, 0))
				if _, err := client.Write(request); err != nil {
					t.Fatal(err)
				}
				response, err := bufio.NewReader(client).ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if err := checkResponse(response, 42); err != nil {
					t.Fatal(err)
				}
				<-time.NewTimer(2 * time.Second).C
				wantReason := "ended"
				if shutdown {
					wantReason = "canceled"
					cancel()
				} else {
					client.Close()
				}
				synctest.Wait()
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				s.Close()
				var opened, closed map[string]any
				for _, r := range loggingRecords(t, &logs) {
					switch r["msg"] {
					case "channel_open":
						if opened != nil {
							t.Fatal("duplicate open")
						}
						opened = r
					case "channel_close":
						if closed != nil {
							t.Fatal("duplicate close")
						}
						closed = r
					}
				}
				if opened == nil || closed == nil {
					t.Fatalf("missing lifecycle: %s", &logs)
				}
				if opened["level"] != "INFO" || opened["local_port"] != float64(14445) || opened["port"] != float64(4700) || opened["client_class"] != "LAN" {
					t.Fatalf("open=%v", opened)
				}
				if closed["level"] != "INFO" || closed["channel"] != opened["channel"] || closed["duration_ms"].(float64) < 2000 || closed["local_to_device_bytes"] != float64(len(request)) || closed["device_to_local_bytes"] != float64(len(response)) || closed["reason"] != wantReason {
					t.Fatalf("close=%v", closed)
				}
			})
		})
	}
}

type loggingFailDialer struct{ err error }

func (d loggingFailDialer) DialPort(context.Context, uint16) (net.Conn, error) { return nil, d.err }

func TestListenerAdmissionFailureLogs(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		err          error
	}{
		{"busy", "busy", session.ErrBusy},
		{"terminal", "timeout", diagnostic.NewTerminal(session.RDT, "timeout", session.ErrTransport)},
		{"unknown", "operation_failed", errors.New("PRIVATE")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var logs bytes.Buffer
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				a, peer := net.Pipe()
				defer peer.Close()
				l := &e2eSingleListener{conn: logAddressConn{a}, done: make(chan struct{})}
				done := make(chan error, 1)
				go func() {
					done <- Serve(ctx, loggingFailDialer{tc.err}, []Forward{{Listener: l, Port: 4700}}, 1, slog.New(slog.NewJSONHandler(&logs, nil)))
				}()
				synctest.Wait()
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				records := loggingRecords(t, &logs)
				if len(records) != 1 || records[0]["msg"] != "client_rejected" || records[0]["reason"] != tc.reason || records[0]["local_port"] != float64(14445) {
					t.Fatalf("rejection=%s", &logs)
				}
			})
		})
	}
}

func TestListenerResetBeforeOpenLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, peer := net.Pipe()
		defer peer.Close()
		reset := make(chan struct{})
		l := &e2eSingleListener{conn: &e2eResetConn{logAddressConn{a}, reset}, done: make(chan struct{})}
		d := &e2ePendingDial{started: make(chan struct{}), canceled: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, d, []Forward{{Listener: l, Port: 4700}}, 1, slog.New(slog.NewJSONHandler(&logs, nil)))
		}()
		<-d.started
		close(reset)
		<-d.canceled
		synctest.Wait()
		cancel()
		<-done
		records := loggingRecords(t, &logs)
		if len(records) != 1 || records[0]["msg"] != "client_canceled" || records[0]["level"] != "INFO" || records[0]["reason"] != "connection_reset" {
			t.Fatalf("reset=%s", &logs)
		}
	})
}

func TestListenerCapacityRejectionLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a, peer := net.Pipe()
		defer peer.Close()
		b, other := net.Pipe()
		defer other.Close()
		l := &admissionQueueListener{conns: make(chan net.Conn), done: make(chan struct{})}
		d := &e2ePendingDial{started: make(chan struct{}), canceled: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- Serve(ctx, d, []Forward{{Listener: l, Port: 4700}}, 1, slog.New(slog.NewJSONHandler(&logs, nil)))
		}()
		defer func() { cancel(); <-done }()
		l.conns <- a
		<-d.started
		l.conns <- logAddressConn{b}
		synctest.Wait()
		records := loggingRecords(t, &logs)
		if len(records) != 1 || records[0]["msg"] != "client_rejected" || records[0]["reason"] != "capacity" || records[0]["local_port"] != float64(14445) {
			t.Fatalf("capacity=%s", &logs)
		}
	})
}

type loggingFailedListener struct{}

func (loggingFailedListener) Accept() (net.Conn, error) { return nil, syscall.EMFILE }
func (loggingFailedListener) Close() error              { return nil }
func (loggingFailedListener) Addr() net.Addr            { return &net.TCPAddr{Port: 14445} }
func TestListenerAcceptFailureLogs(t *testing.T) {
	var logs bytes.Buffer
	err := Serve(context.Background(), loggingFailDialer{}, []Forward{{Listener: loggingFailedListener{}, Port: 4700}}, 1, slog.New(slog.NewJSONHandler(&logs, nil)))
	records := loggingRecords(t, &logs)
	if err == nil || len(records) != 1 || records[0]["msg"] != "listener_accept_failed" || records[0]["reason"] != "descriptor_limit" || records[0]["local_port"] != float64(14445) {
		t.Fatalf("accept=%s err=%v", &logs, err)
	}
}

func TestClientAddressClasses(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{
		{"127.0.0.1", "loopback"}, {"::1", "loopback"}, {"192.168.1.2", "LAN"},
		{"10.23.4.5", "LAN"}, {"172.20.0.1", "LAN"}, {"fd00::123", "LAN"},
		{"fe80::123", "LAN"}, {"169.254.1.2", "LAN"}, {"203.0.113.5", "other"}, {"2001:db8::1", "other"},
	} {
		if got := clientClass(&net.TCPAddr{IP: net.ParseIP(tc.ip)}); got != tc.want {
			t.Errorf("class of %s=%s want %s", tc.ip, got, tc.want)
		}
	}
	if got := clientClass(nil); got != "other" {
		t.Errorf("nil address=%s want other", got)
	}
}

type loggingPartialWriter struct{ net.Conn }

func (loggingPartialWriter) Write([]byte) (int, error) { return 3, syscall.EPIPE }
func TestBridgeSummaryPartialWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, local := net.Pipe()
		defer local.Close()
		b, device := net.Pipe()
		defer device.Close()
		result := bridgeReader(a, loggingPartialWriter{b}, strings.NewReader("request"), false, nil)
		if result.localToDevice != 3 || result.deviceToLocal != 0 || result.reason != "broken_pipe" {
			t.Fatalf("summary=%+v want 3/0/broken_pipe", result)
		}
	})
}
