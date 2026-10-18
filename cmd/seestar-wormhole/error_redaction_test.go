package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/keylog"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Verify returned errors do not expose sensitive paths or dependency details.
func assertNoSensitiveErrorDetails(t *testing.T, err error, secrets ...string) {
	t.Helper()
	var path *os.PathError
	var op *net.OpError
	var dns *net.DNSError
	if errors.As(err, &path) || errors.As(err, &op) || errors.As(err, &dns) {
		t.Errorf("raw dependency cause reachable through errors.As: %T", err)
	}
	var visit func(error)
	visit = func(e error) {
		if e == nil {
			return
		}
		for _, secret := range secrets {
			for _, s := range []string{e.Error(), fmt.Sprintf("%+v", e), fmt.Sprintf("%#v", e)} {
				if strings.Contains(s, secret) {
					t.Errorf("secret %q reachable via %T: %s", secret, e, s)
				}
			}
		}
		switch x := e.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range x.Unwrap() {
				visit(child)
			}
		case interface{ Unwrap() error }:
			visit(x.Unwrap())
		}
	}
	visit(err)
}
func setValidCommandEnvironment(t *testing.T) {
	t.Helper()
	for _, k := range []string{"SEESTAR_EMAIL", "SEESTAR_PASSWORD"} {
		t.Setenv(k, "")
	}
}
func availableCommandPort(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return strconv.Itoa(port)
}

type cancelOnLogWrite struct {
	mu     sync.Mutex
	b      bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelOnLogWrite) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, e := w.b.Write(p)
	if bytes.Contains(p, []byte("proxy_started")) {
		w.cancel()
	}
	return n, e
}
func TestKeylogAttribution(t *testing.T) {
	setValidCommandEnvironment(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &cancelOnLogWrite{cancel: cancel}
	err := runWithSession(ctx, append([]string{"proxy"}, []string{"--idle=2m", "--bind=127.0.0.1", "--port", availableCommandPort(t) + ":445", "--keylog=/nonexistent/PRIVATE_KEYLOG_PATH"}...), io.Discard, logs, keylog.Open, idleCommandFixture)
	logs.mu.Lock()
	data := append([]byte(nil), logs.b.Bytes()...)
	logs.mu.Unlock()
	t.Logf("return=%v log=%s", err, data)
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	seen := false
	for {
		var r map[string]any
		e := json.UnmarshalDecode(dec, &r, json.MatchCaseInsensitiveNames(true))
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if r["msg"] == "keylog_failed" {
			seen = true
			if r["option"] != "--keylog" || r["rule"] == nil {
				t.Errorf("startup keylog failure lacks option/rule: %v", r)
			}
		}
	}
	if !seen {
		t.Error("best-effort creation failure lost warning")
	}
	if !bytes.Contains(data, []byte("proxy_started")) {
		t.Error("best-effort failure rejected previously accepted startup")
	}
	if bytes.Contains(data, []byte("PRIVATE_KEYLOG_PATH")) {
		t.Error("keylog path leaked")
	}
	assertNoSensitiveErrorDetails(t, err, "PRIVATE_KEYLOG_PATH")
}
