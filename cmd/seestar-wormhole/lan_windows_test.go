//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
)

func TestWindowsLANRefusalBeforeResources(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reserved.Close()
	{
		for _, bind := range []string{"0.0.0.0", "127.0.0.2"} {
			t.Run(bind, func(t *testing.T) {
				trace := filepath.Join(root)
				args := []string{"--lan-discovery", "--bind", bind}
				for _, port := range []int{4700, 445, 80, 4350, 4554, 4555, 4801, 4800, 4804, 4361} {
					args = append(args, "--port", "0:"+strconv.Itoa(port))
				}
				args = append(args, "--port", strconv.Itoa(reserved.Addr().(*net.TCPAddr).Port)+":13459")
				var logs bytes.Buffer
				err := runWithListener(context.Background(), append([]string{"proxy"}, args...), io.Discard, &logs, func(context.Context, proxy.Config) (commandSession, error) {
					t.Fatal("session created")
					return nil, nil
				}, func(string, string) (net.Listener, error) {
					t.Fatal("listener opened")
					return nil, nil
				})
				var detail *argumentError
				if !errors.As(err, &detail) || detail.Option != "--lan-discovery" || !strings.Contains(detail.Rule, "Windows") || !strings.Contains(detail.Rule, "unsupported") {
					t.Fatalf("got %v (%s), want Windows unsupported --lan-discovery argument error", err, &logs)
				}
				// Also use the real command wiring against the reserved TCP port.
				logs.Reset()
				err = run(context.Background(), append([]string{"proxy"}, args...), io.Discard, &logs)
				if !errors.As(err, &detail) || detail.Option != "--lan-discovery" || detail.Rule != "LAN discovery is unsupported on Windows" {
					t.Fatalf("reserved-port command: %v (%s)", err, &logs)
				}
				{
					if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("capture created: %s: %v", path, err)
					}
				}
			})
		}
	}
}
