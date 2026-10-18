package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
)

type stalledKeys struct{ entered, release chan struct{} }

func (s stalledKeys) Write(p []byte) (int, error) { close(s.entered); <-s.release; return len(p), nil }
func (stalledKeys) Close() error                  { return nil }

func TestCommandReturnsWithStalledKeylog(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mapping := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + ":80"
	synctest.Test(t, func(t *testing.T) {
		sink := stalledKeys{make(chan struct{}), make(chan struct{})}
		defer close(sink.release)
		var logs bytes.Buffer
		open := func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
			keys := keylog.New(sink, logger)
			keys.Write([]byte("test line\n"))
			<-sink.entered
			return keys, nil
		}
		start := time.Now()
		err := runWithKeyLog(context.Background(), append([]string{"proxy"}, []string{"--keylog", "secret-path", "--bind", "127.0.0.1", "--port", mapping}...), io.Discard, &logs, open)
		if err == nil {
			t.Fatal("expected listener failure")
		}
		if elapsed := time.Since(start); elapsed != keylog.ShutdownBound {
			t.Fatalf("command return took %v", elapsed)
		}
		if strings.Contains(logs.String(), "secret-path") {
			t.Fatal("path leaked")
		}
	})
}

func TestKeylogOpenFailureDoesNotFailCommand(t *testing.T) {
	setValidCommandEnvironment(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &cancelOnLogWrite{cancel: cancel}
	open := func(string, *slog.Logger) (*keylog.Writer, error) { return nil, errors.New("secret-path") }
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--idle=2m", "--keylog", "secret-path", "--bind", "127.0.0.1", "--port", availableCommandPort(t) + ":80"}...), io.Discard, logs, open, idleCommandFixture)
	if err != nil || !strings.Contains(logs.b.String(), "proxy_started") {
		t.Fatalf("capture failure stopped startup: %v %s", err, &logs.b)
	}
	if strings.Count(logs.b.String(), "keylog_failed") != 1 || !strings.Contains(logs.b.String(), `"option":"--keylog"`) || strings.Contains(logs.b.String(), "secret-path") {
		t.Fatal(logs.b.String())
	}
}

// Exercise the real worker's one-shot warning both before startup resolution
// and during Close, so option failures stay quiet without losing other warnings.
type failedStartupKeys struct{}

func (failedStartupKeys) Write([]byte) (int, error) { return 0, errors.New("PRIVATE_PAYLOAD") }
func (failedStartupKeys) Close() error              { return errors.New("PRIVATE_KEYLOG_PATH") }

func TestKeylogWorkerStartupDiagnostic(t *testing.T) {
	t.Setenv("SEESTAR_EMAIL", "")
	t.Setenv("SEESTAR_PASSWORD", "PRIVATE_PASSWORD")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupied := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + ":80"
	for _, mapping := range []string{occupied, "12345:80"} {
		t.Run(mapping, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var out, logs bytes.Buffer
				open := func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
					keys := keylog.New(failedStartupKeys{}, logger)
					keys.Write([]byte("PRIVATE_PAYLOAD\n"))
					synctest.Wait() // The reporter has emitted its initial failure before run continues.
					return keys, nil
				}
				err := runWithKeyLog(context.Background(), append([]string{"proxy"}, []string{"--keylog", "PRIVATE_KEYLOG_PATH", "--bind", "127.0.0.1", "--port", mapping}...), &out, &logs, open)
				if err == nil {
					t.Fatal("invalid startup accepted")
				}
				if mapping == occupied {
					assertStartupOptionFailure(t, err, &out, &logs, "--bind", "use an OS-supported bind address with available local ports", "127.0.0.1", "PRIVATE_PAYLOAD", "PRIVATE_KEYLOG_PATH", mapping)
				} else {
					assertStartupOptionFailure(t, err, &out, &logs, "SEESTAR_EMAIL", "use a valid UTF-8 email of at most 4096 bytes matching the supported account format", "PRIVATE_PAYLOAD", "PRIVATE_KEYLOG_PATH", "PRIVATE_PASSWORD")
				}
			})
		})
	}
}

func TestKeylogWorkerAcceptedStartupAttribution(t *testing.T) {
	setValidCommandEnvironment(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &cancelOnLogWrite{cancel: cancel}
	open := func(_ string, logger *slog.Logger) (*keylog.Writer, error) {
		keys := keylog.New(failedStartupKeys{}, logger)
		keys.Write([]byte("PRIVATE_PAYLOAD\n"))
		return keys, nil
	}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--idle=2m", "--keylog", "PRIVATE_KEYLOG_PATH", "--bind=127.0.0.1", "--port", availableCommandPort(t) + ":80"}...), io.Discard, logs, open, idleCommandFixture)
	if err == nil {
		t.Fatal("keylog loss returned command success")
	}
	data := logs.b.String()
	if strings.Count(data, `"msg":"keylog_failed"`) < 1 || !strings.Contains(data, `"rule":"keylog_loss"`) || !strings.Contains(data, "proxy_started") {
		t.Fatalf("missing accepted worker attribution: %s", data)
	}
	for _, value := range []string{"PRIVATE_PAYLOAD", "PRIVATE_KEYLOG_PATH"} {
		if strings.Contains(data, value) {
			t.Fatalf("worker value leaked: %s", data)
		}
	}
}
