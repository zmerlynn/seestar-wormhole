package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
