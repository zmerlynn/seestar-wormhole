package ui

import (
	"context"
	"encoding/json/v2"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/proxy"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

func TestCatalogEventsAndPrivateLog(t *testing.T) {
	st := testStorage(t)
	l := &eventLog{storage: st}
	for _, c := range []cloudhttp.Catalog{
		{Pairs: []cloudhttp.CatalogPair{{Model: "S30", Serial: "synthetic-one"}}, Count: 1, Complete: true},
		{Pairs: []cloudhttp.CatalogPair{{Model: "", Serial: "synthetic-two"}}, Count: 1, Complete: false},
		{Pairs: []cloudhttp.CatalogPair{}, Count: 0, Complete: false},
	} {
		message := c.Message()
		if eventCode(message, "", c) != "lifecycle" {
			t.Fatal("catalog lifecycle rejected")
		}
		for _, arbitrary := range []string{message + " arbitrary", "arbitrary " + message, "Account telescopes: private error"} {
			if eventCode(arbitrary, "", c) != "" {
				t.Fatal("arbitrary catalog accepted")
			}
		}
		if eventCode(message, "") != "" {
			t.Fatal("catalog accepted without explicit context")
		}
		l.addCatalog(c)
		l.addCatalog(c)
	}
	events, _, failed := l.snapshot()
	if failed || len(events) != 6 {
		t.Fatalf("events=%+v failed=%v", events, failed)
	}
	for i, e := range events {
		if e.Code != "lifecycle" || e.Message != events[i/2*2].Message {
			t.Fatal("catalog event changed")
		}
	}
	b, err := os.ReadFile(filepath.Join(st.root, "gui.log"))
	if err != nil {
		t.Fatal(err)
	}
	records := 0
	for _, line := range strings.Split(string(b), "\n") {
		var record struct {
			Msg, Level string
			cloudhttp.Catalog
		}
		if json.Unmarshal([]byte(line), &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "device_catalog" {
			continue
		}
		records++
		if record.Level != "INFO" || record.Count != len(record.Pairs) || record.Omitted != 0 {
			t.Fatalf("private catalog=%+v", record)
		}
		if record.Count == 1 && record.Pairs[0].Serial != "synthetic-one" && record.Pairs[0].Serial != "synthetic-two" {
			t.Fatal("private log lost full serial")
		}
	}
	if records != 6 {
		t.Fatalf("private catalog count=%d", records)
	}
	for range eventLimit {
		l.addCatalog(cloudhttp.Catalog{})
	}
	events, _, _ = l.snapshot()
	if len(events) != eventLimit {
		t.Fatal("event bound changed")
	}
}

func TestControllerCatalogEveryConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testController(func(_ context.Context, _ Settings, _ cloud.Credentials, status *proxy.Status) error {
			status.RecordCatalog(cloudhttp.Catalog{Pairs: []cloudhttp.CatalogPair{{Model: "S30", Serial: "synthetic-one"}}, Count: 1, Complete: true})
			return nil
		})
		c.log = &eventLog{}
		for range 2 {
			if err := c.start(testSettings(), testPassword); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
		}
		events, _, _ := c.log.snapshot()
		count := 0
		for _, e := range events {
			if e.Message == "Account telescopes: S30 synthetic-one" && e.Code == "lifecycle" {
				count++
			}
		}
		if count != 2 {
			t.Fatalf("catalog events per Connect=%d", count)
		}
	})
}
