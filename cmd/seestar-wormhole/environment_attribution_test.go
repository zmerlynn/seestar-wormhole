package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"runtime"
	"testing"
	"time"
)

// Adopted from the round-two environment-attribution and late-template probes.
func TestEnvironmentAttribution(t *testing.T) {
	for _, tc := range []struct{ name, email, password string }{
		{"missing-email", "", "PRIVATE_PASSWORD"},
		{"missing-password", "test@example.invalid", ""},
		{"invalid-email", "PRIVATE_INVALID_EMAIL", "PRIVATE_PASSWORD"},
		{"invalid-password", "test@example.invalid", "short"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setValidCommandEnvironment(t)
			t.Setenv("SEESTAR_EMAIL", tc.email)
			t.Setenv("SEESTAR_PASSWORD", tc.password)

			var out, logs bytes.Buffer
			err := run(context.Background(), append([]string{"proxy"}, []string{"--bind=127.0.0.1", "--port", availableCommandPort(t) + ":445"}...), &out, &logs)
			t.Logf("error=%v stdout=%q log=%s", err, out.String(), &logs)
			if !errors.Is(err, cloud.ErrConfig) {
				t.Fatalf("invalid env configuration accepted: %v", err)
			}
			var record map[string]any
			if e := json.Unmarshal(logs.Bytes(), &record, json.MatchCaseInsensitiveNames(true)); e != nil {
				t.Fatal(e)
			}
			setting := map[string]string{"missing-email": "SEESTAR_EMAIL", "missing-password": "SEESTAR_PASSWORD", "invalid-email": "SEESTAR_EMAIL", "invalid-password": "SEESTAR_PASSWORD"}[tc.name]
			rule := "use a nonblank opaque value of at most 4096 bytes without NUL, CR or LF"
			if setting == "SEESTAR_EMAIL" {
				rule = "use a valid UTF-8 email of at most 4096 bytes matching the supported account format"
			}
			if setting == "SEESTAR_PASSWORD" {
				rule = "use a valid UTF-8 password of 8..32 UTF-16 units"
			}
			if record["option"] != setting || record["rule"] != rule || record["help"] != "seestar-wormhole proxy --help" || record["phase"] != "arguments" || record["reason"] != "invalid_option" {
				t.Errorf("wrong environment diagnostic: %v", record)
			}
			var detail *diagnostic.OptionError
			if !errors.As(err, &detail) || detail.Option != setting || detail.Rule != rule {
				t.Errorf("wrong returned option error: %v", err)
			}
			assertNoSensitiveErrorDetails(t, err, "PRIVATE_PASSWORD", "PRIVATE_INVALID_EMAIL", "PRIVATE_TOKEN", "PRIVATE_SOURCE")
			if bytes.Contains(logs.Bytes(), []byte("PRIVATE_")) || out.Len() != 0 {
				t.Error("private environment value leaked")
			}
		})
	}
}

func TestSelectorAttribution(t *testing.T) {
	setValidCommandEnvironment(t)

	for _, tc := range []struct{ name, key, value string }{
		{"serial-control", "--serial", "PRIVATE_SERIAL\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {

			var out, logs bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			args := []string{"--bind=0.0.0.0", "--port", availableCommandPort(t) + ":13459", "--idle=2m", "--serial", tc.value}
			if runtime.GOOS != "windows" {
				args = append(args, "--lan-discovery")
			}
			err := run(ctx, append([]string{"proxy"}, args...), &out, &logs)
			t.Logf("returned=%v stdout=%q log=%s", err, out.String(), &logs)
			if err == nil {
				t.Errorf("invalid selector should fail safely: %v", err)
			}
			assertNoSensitiveErrorDetails(t, err, "PRIVATE_SERIAL", "PRIVATE_MODEL")
			if bytes.Contains(logs.Bytes(), []byte("PRIVATE_")) || out.Len() != 0 {
				t.Error("selector value leaked")
			}
			decoder := jsontext.NewDecoder(&logs)
			found := false
			for {
				var record map[string]any
				e := json.UnmarshalDecode(decoder, &record, json.MatchCaseInsensitiveNames(true))
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				if record["msg"] == "proxy failed" {
					found = true
					if record["option"] != tc.key || record["rule"] == nil {
						t.Errorf("selector failure lacks option/rule: %v", record)
					}
				}
			}
			if !found {
				t.Error("missing final diagnostic")
			}
		})
	}
}
