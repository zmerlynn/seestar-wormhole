package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/zmerlynn/seestar-wormhole/internal/landiscovery"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func discoveryArgs() []string {
	return []string{"discovery", "--bind=192.0.2.2", "--advertise-serial=TEST-SERIAL", "--advertise-model=TEST-MODEL"}
}

func TestDiscoveryFlags(t *testing.T) {
	for _, bind := range []string{"192.0.2.2", "0.0.0.0"} {
		o, err := parse(append(discoveryArgs(), "--bind="+bind, "--lan-discovery-all-interfaces"), io.Discard)
		if err != nil || o.bind != bind || o.serial != "TEST-SERIAL" || o.model != "TEST-MODEL" || !o.lanDiscoveryAllInterfaces || len(o.ports) != 0 {
			t.Fatalf("discovery flags: options=%+v err=%v", o, err)
		}
	}
}

func TestDiscoveryInvalidOptions(t *testing.T) {
	for _, tc := range []struct {
		name, option string
		args         []string
	}{
		{"missing serial", "--advertise-serial", []string{"discovery", "--bind=192.0.2.2", "--advertise-model=TEST-MODEL"}},
		{"missing model", "--advertise-model", []string{"discovery", "--bind=192.0.2.2", "--advertise-serial=TEST-SERIAL"}},
		{"empty serial", "--advertise-serial", append(discoveryArgs(), "--advertise-serial=")},
		{"empty model", "--advertise-model", append(discoveryArgs(), "--advertise-model=")},
		{"blank serial", "--advertise-serial", append(discoveryArgs(), "--advertise-serial= \t")},
		{"blank model", "--advertise-model", append(discoveryArgs(), "--advertise-model= \t")},
		{"missing bind", "--bind", append(discoveryArgs(), "--bind=")},
		{"hostname", "--bind", append(discoveryArgs(), "--bind=PRIVATE_HOST")},
		{"loopback", "--bind", append(discoveryArgs(), "--bind=127.0.0.1")},
		{"IPv6", "--bind", append(discoveryArgs(), "--bind=::")},
		{"multicast", "--bind", append(discoveryArgs(), "--bind=224.0.0.1")},
		{"broadcast", "--bind", append(discoveryArgs(), "--bind=255.255.255.255")},
		{"oversize identity", "arguments", append(discoveryArgs(), "--advertise-name="+strings.Repeat("PRIVATE_NAME", 500))},
		{"invalid UTF-8", "arguments", append(discoveryArgs(), "--advertise-name=\xff")},
		{"format", "--log-format", append(discoveryArgs(), "--log-format=PRIVATE_FORMAT")},
		{"level", "--log-level", append(discoveryArgs(), "--log-level=PRIVATE_LEVEL")},
		{"catalog serial", "arguments", append(discoveryArgs(), "--serial=PRIVATE_SERIAL")},
		{"TCP port", "arguments", append(discoveryArgs(), "--port=80:80")},
		{"positional", "arguments", append(discoveryArgs(), "PRIVATE_ARGUMENT")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, logs bytes.Buffer
			err := run(context.Background(), tc.args, &out, &logs)
			var detail *argumentError
			if !errors.As(err, &detail) || commandExitCode(options{}, err) != 2 {
				t.Fatalf("want configuration refusal, got %v", err)
			}
			var rec struct{ Msg, Phase, Reason, Option, Help string }
			if e := json.Unmarshal(logs.Bytes(), &rec, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatalf("want one JSON diagnostic: %v, logs=%q", e, &logs)
			}
			if out.Len() != 0 || rec.Msg != "discovery failed" || rec.Phase != "arguments" || rec.Reason != "invalid_option" || rec.Option != tc.option || rec.Help != "seestar-wormhole discovery --help" || strings.Contains(logs.String(), "PRIVATE_") {
				t.Fatalf("configuration diagnostic: stdout=%q logs=%q", &out, &logs)
			}
		})
	}
}

// Use the same commandDiscovery seam as proxy startup tests. Socket admission
// and real UDP source/port behavior are exercised in internal/landiscovery.
type scanCommandDiscovery struct {
	replies chan []byte
	closed  bool
}

func (s *scanCommandDiscovery) Serve(ctx context.Context, m *landiscovery.Machine) error {
	for _, scan := range []string{
		"{\"id\":7,\"method\":\"scan_iscope\",\"params\":\"\"}\r\n",
		"{\"id\":8,\"method\":\"scan_iscope\",\"name\":\"ignored\",\"ip\":\"ignored\"}\r\n",
	} {
		s.replies <- m.Step([]byte(scan), 0)
	}
	<-ctx.Done()
	return nil
}

func (s *scanCommandDiscovery) Close() error { s.closed = true; return nil }

func TestDiscoveryScanWithoutCredentialsOrSession(t *testing.T) {
	for _, credentials := range []string{"unset", "invalid"} {
		for _, name := range []string{"", "TEST-NAME"} {
			t.Run(credentials+"/"+name, func(t *testing.T) {
				for _, key := range []string{"SEESTAR_EMAIL", "SEESTAR_PASSWORD"} {
					t.Setenv(key, "PRIVATE_INVALID_CREDENTIAL")
					if credentials == "unset" {
						if err := os.Unsetenv(key); err != nil {
							t.Fatal(err)
						}
					}
				}
				// Discovery must also ignore unrelated proxy environment options.
				t.Setenv("SEESTAR_STATE_DIR", "PRIVATE_INVALID_STATE")
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				s := &scanCommandDiscovery{replies: make(chan []byte, 2)}
				var out, logs bytes.Buffer
				done := make(chan int, 1)
				go func() {
					done <- mainExitCodeWithRunner(append(discoveryArgs(), "--advertise-name="+name, "--lan-discovery-all-interfaces"), &out, &logs,
						func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) { return ctx, cancel },
						func() (io.Writer, func()) { return &logs, func() {} }, false,
						func(ctx context.Context, o options, stdout, stderr io.Writer) error {
							if o.command != commandDiscoveryOnly {
								t.Errorf("wrong dispatch: %v", o.command)
							}
							return runParsedWithDiscovery(ctx, o, stdout, stderr,
								func(context.Context, proxy.Config) (commandSession, error) {
									t.Error("discovery constructed a session")
									return nil, errors.New("unexpected session")
								}, func(string, string) (net.Listener, error) {
									t.Error("discovery opened a TCP listener")
									return nil, errors.New("unexpected TCP listener")
								}, func(bind string, port int, options landiscovery.Options, logger *slog.Logger) (commandDiscovery, error) {
									if bind != "192.0.2.2" || port != landiscovery.Port || !options.AllInterfaces || logger == nil {
										t.Errorf("discovery listen: bind=%q port=%d options=%+v logger=%v", bind, port, options, logger)
									}
									return s, nil
								})
						})
				}()
				for _, id := range []int{7, 8} {
					select {
					case b := <-s.replies:
						var rec struct {
							ID     int `json:"id"`
							Result struct {
								Serial string `json:"sn"`
								Name   string `json:"ssid"`
								Model  string `json:"product_model"`
							} `json:"result"`
						}
						wantName := name
						if wantName == "" {
							wantName = "TEST-MODEL"
						}
						if err := json.Unmarshal(b, &rec); err != nil || rec.ID != id || rec.Result.Serial != "TEST-SERIAL" || rec.Result.Model != "TEST-MODEL" || rec.Result.Name != wantName {
							t.Errorf("scan/reply: reply=%s err=%v", b, err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("discovery did not answer scan")
					}
				}
				cancel()
				select {
				case code := <-done:
					if code != 0 || !s.closed || out.Len() != 0 || !strings.Contains(logs.String(), "lan_discovery_started") || !strings.Contains(logs.String(), "lan_discovery_synthesized") || strings.Contains(logs.String(), "failed") || strings.Contains(logs.String(), "PRIVATE_") {
						t.Fatalf("shutdown: exit=%d closed=%t stdout=%q logs=%q", code, s.closed, &out, &logs)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("discovery did not join on cancellation")
				}
			})
		}
	}
}

type failedCommandDiscovery struct{ closed bool }

func (*failedCommandDiscovery) Serve(context.Context, *landiscovery.Machine) error {
	return io.ErrUnexpectedEOF
}
func (s *failedCommandDiscovery) Close() error { s.closed = true; return nil }

func TestDiscoveryFailureRecords(t *testing.T) {
	for _, failListen := range []bool{true, false} {
		var logs bytes.Buffer
		o, err := parse(append(discoveryArgs(), "--log-format=text", "--log-level=error"), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		s := &failedCommandDiscovery{}
		err = runDiscovery(t.Context(), o, &logs, func(string, int, landiscovery.Options, *slog.Logger) (commandDiscovery, error) {
			if failListen {
				return nil, errors.New("PRIVATE_OS_ERROR")
			}
			return s, nil
		})
		wantCode, wantPhase, wantReason := 1, "lan_discovery", "operation_failed"
		if failListen {
			wantCode, wantPhase, wantReason = 2, "arguments", "invalid_option"
		}
		var rec struct {
			Msg, Phase, Reason string
			CleanupReason      string `json:"cleanup_reason"`
		}
		if e := json.Unmarshal(logs.Bytes(), &rec, json.MatchCaseInsensitiveNames(true)); e != nil {
			t.Fatal(e, &logs)
		}
		if commandExitCode(o, err) != wantCode || rec.Msg != "discovery failed" || rec.Phase != wantPhase || rec.Reason != wantReason || rec.CleanupReason != "none" || (!failListen && !s.closed) || strings.Contains(logs.String(), "PRIVATE_") {
			t.Fatalf("failure: err=%v closed=%t logs=%q", err, s.closed, &logs)
		}
	}
}

func TestDiscoveryHelpWithoutIdentity(t *testing.T) {
	for _, args := range [][]string{{"discovery", "--help"}, {"help", "discovery"}} {
		var out, logs bytes.Buffer
		if err := run(t.Context(), args, &out, &logs); !errors.Is(err, errHelp) || logs.Len() != 0 || !strings.Contains(out.String(), "--advertise-model") {
			t.Fatalf("discovery help: err=%v out=%q logs=%q", err, &out, &logs)
		}
	}
}
