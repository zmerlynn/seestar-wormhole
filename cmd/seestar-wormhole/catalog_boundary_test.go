package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json/v2"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Exercise the HTTP boundary with the vendor origin mapped to loopback.
// Real TLS/HTTP edge and synthetic read-only response; never contacts the service.
func TestCatalogBoundary(t *testing.T) {
	if os.Getenv("ROUND3_LOOPBACK_TLS") != "1" {
		t.Skip("requires disposable container with vendor origin mapped to loopback")
	}
	if os.Getenv("ROUND4_CATALOG_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCatalogBoundary$", "-test.v")
		cmd.Env = append(os.Environ(), "ROUND4_CATALOG_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("loopback catalog probe: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	ips, e := net.LookupHost("api-ithings.zwoastro.com")
	if e != nil || len(ips) != 1 || ips[0] != "127.0.0.1" {
		t.Fatal("fixture origin must resolve only to loopback")
	}
	setValidCommandEnvironment(t)
	t.Setenv("SEESTAR_EMAIL", "fixture@example.invalid")
	t.Setenv("SEESTAR_PASSWORD", "fixture-password")

	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "round2 fixture"}, DNSNames: []string{"api-ithings.zwoastro.com", "api.seestar.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, pub, priv)
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	ca := filepath.Join(root, "PRIVATE_CA_PATH.pem")
	if e := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("SSL_CERT_FILE", ca)
	t.Setenv("SSL_CERT_DIR", root)
	l, e := tls.Listen("tcp", "127.0.0.1:443", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}})
	if e != nil {
		t.Fatal(e)
	}
	var detailRequests atomic.Int32
	var requestedModel atomic.Value
	requestedModel.Store("")
	var detailReply atomic.Value
	detailReply.Store(`{"data":{"deviceSn":"SYNTHETIC_SN","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"fixture"}}`)
	var catalog atomic.Value
	catalog.Store(`{"code":200,"data":{"items":[],"total":0}}`)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/login" {
			io.WriteString(w, `{"code":0,"data":{"accessToken":"fixture-token"}}`)
			return
		}
		if r.URL.Path == "/v1/user/current-profile" {
			io.WriteString(w, `{"code":0,"data":{"id":"fixture-account"}}`)
			return
		}
		if r.URL.Path == "/v1/my-device" {
			if r.URL.Query().Get("current") != "1" {
				t.Error("unexpected catalog page")
				w.WriteHeader(http.StatusBadRequest)
			} else {
				io.WriteString(w, catalog.Load().(string))
			}
			return
		}
		if r.URL.Path != "/v1/my-device/detail" {
			w.WriteHeader(401)
			io.WriteString(w, `{"code":401,"msg":"fixture refuses allocation"}`)
			return
		}
		detailRequests.Add(1)
		requestedModel.Store(r.URL.Query().Get("deviceModel"))
		io.WriteString(w, detailReply.Load().(string))
	})}
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(l) }()
	defer func() { srv.Close(); <-done }()

	for _, tc := range []struct{ name, serial, body string }{
		{"wrong-serial", "PRIVATE_WRONG_SERIAL", `{"code":200,"data":{"items":[{"deviceSn":"SYNTHETIC_SN","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"}],"total":1}}`},
		{"ambiguous-catalog", "", `{"code":200,"data":{"items":[{"deviceSn":"SYNTHETIC_SN_A","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi"},{"deviceSn":"SYNTHETIC_SN_B","deviceModel":"S30","deviceStatus":1,"ssid":"synthetic-wifi"}],"total":2}}`},
		{"ambiguous-serial", "SYNTHETIC_SN", `{"data":{"total":2,"items":[{"deviceSn":"SYNTHETIC_SN","deviceModel":"S50 Pro","deviceStatus":2,"ssid":"first"},{"deviceSn":"SYNTHETIC_SN","deviceModel":"S30","deviceStatus":1,"ssid":"second"}]}}`},
		{"empty-catalog", "", `{"code":200,"data":{"items":[],"total":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog.Store(tc.body)
			wantOption, wantRule := "--serial", "select an existing device from the account catalog"
			if tc.name == "empty-catalog" {
				wantOption, wantRule = "--model", "incomplete catalog; name --model to select a received pair"
			}
			if tc.name == "ambiguous-serial" {
				wantOption, wantRule = "--model", "serial is ambiguous; name --model to select a catalog pair"
			}

			for _, format := range []string{"json", "text"} {
				t.Run(format, func(t *testing.T) {
					var out, logs bytes.Buffer
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					args := []string{"--idle=2m", "--bind=127.0.0.1", "--port", availableCommandPort(t) + ":445", "--log-format", format, "--log-level=debug", "--serial", tc.serial}
					err := run(ctx, append([]string{"proxy"}, args...), &out, &logs)
					t.Logf("error=%v stdout=%q log=%s", err, out.String(), &logs)
					if !errors.Is(err, cloud.ErrConfig) {
						t.Fatalf("want config error, got %v", err)
					}
					assertNoSensitiveErrorDetails(t, err, "PRIVATE_WRONG_SERIAL", "SYNTHETIC_TOKEN", "SYNTHETIC_SOURCE")
					var detail *diagnostic.OptionError
					if !errors.As(err, &detail) || detail.Option != wantOption || detail.Rule != wantRule {
						t.Errorf("catalog-dependent configuration failure lacks option/rule: %v", err)
					}
					var rec map[string]any
					if e := json.Unmarshal(logs.Bytes(), &rec, json.MatchCaseInsensitiveNames(true)); e != nil {
						t.Errorf("want exactly one attributed JSON error before dependencies: %v", e)
					} else if rec["option"] != wantOption || rec["rule"] != wantRule || rec["level"] != "ERROR" || rec["reason"] != "invalid_option" || rec["phase"] != "arguments" || rec["help"] != "seestar-wormhole proxy --help" || out.Len() != 0 {
						t.Errorf("wrong final diagnostic: %v", rec)
					}
				})
			}
		})
	}

	// Check Config.Model reaches real catalog selection in proxy.New, using the
	// synchronous read-only LAN detail precheck before any device session exists.
	catalog.Store(`{"data":{"total":2,"items":[{"deviceSn":"SYNTHETIC_SN","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"first"},{"deviceSn":"SYNTHETIC_SN","deviceModel":"S30","deviceStatus":2,"ssid":"second"}]}}`)
	detailReply.Store(`{"data":{"deviceSn":"SYNTHETIC_SN","deviceModel":"S30","deviceStatus":4,"ssid":"fixture"}}`)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s, err := proxy.New(ctx, proxy.Config{Serial: "SYNTHETIC_SN", Model: "S30", LANDiscovery: true})
	if s != nil {
		s.Close()
		t.Fatal("unavailable detail started session")
	}
	if !errors.Is(err, session.ErrBusy) || detailRequests.Load() != 1 || requestedModel.Load() != "S30" {
		t.Fatalf("config pair wiring: error=%v detail count=%d model=%v", err, detailRequests.Load(), requestedModel.Load())
	}

}
