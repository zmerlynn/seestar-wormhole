package cloudhttp

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/iotc"
)

// Literal inputs are independent of our encoders. All identities and secrets are
// synthetic. The license input is decoded JSON, not a minted service signature.
var metamorphicFixtures = []struct{ name, body string }{
	{"login", `{"msg":"ok","data":{"accessToken":"synthetic-token-sentinel","accessTokenType":"opaque","accessExpire":10,"refreshAfter":5},"code":200}`},
	{"profile", `{"msg":"ok","data":{"id":"synthetic-profile-sentinel","country":{"name":"Example","code":"XX"},"whitelistFunctions":["example"]},"code":200}`},
	{"devices", `{"msg":"ok","data":{"total":1,"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-wifi","location":{"city":"Example"}}]},"code":200}`},
	{"detail", `{"msg":"ok","data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-ssid-sentinel","location":{"city":"Example"}},"code":200}`},
	{"lan_detail", `{"msg":"ok","data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":3,"ssid":"synthetic-ssid-sentinel","location":{"city":"Example"}},"code":200}`},
	{"allocation", `{"msg":"ok","data":{"conId":"synthetic-conid-sentinel","deviceUid":"SYNTHETICUID00000001","sdkLicenseKey":"synthetic-license-sentinel","username":"synthetic-user-sentinel","password":"synthetic-password-sentinel","p2pFactory":"example","p2pLicenseKey":"unused","p2pStatus":1},"code":200}`},
	{"stop", `{"msg":"ok","data":{"unused":[{"value":1}]},"code":200}`},
	{"license", `{"standalone":{"eu":"eu.example.invalid","all":"all.example.invalid"},"keyMask":"AAECAwQFBgcICQoLDA0ODw==","realm":"example","nebulaDomainFromLicense":false}`},
}

// Unused DTO members whose absence and null have the same consumed meaning.
var metamorphicOptionalFields = map[string][]string{
	"login":      {"accessTokenType", "accessExpire", "refreshAfter", "tokenType"},
	"profile":    {"country", "whitelistFunctions"},
	"devices":    {"total", "current"},
	"allocation": {"p2pFactory", "p2pLicenseKey", "p2pStatus"},
}

func metamorphicObject(t *testing.T, b []byte) map[string]jsontext.Value {
	t.Helper()
	var v map[string]jsontext.Value
	if err := json.Unmarshal(b, &v, json.MatchCaseInsensitiveNames(true)); err != nil || v == nil {
		t.Fatal("invalid literal object")
	}
	return v
}

func metamorphicJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal("cannot encode fixture mutation")
	}
	return b
}

// No JSON walker/tokenizer: encoding/json decodes only explicitly selected
// fixture locations. Controls bound the generator to four added fields per
// selected object, depth three, and four extra ignored array entries.
// Bits select unknown members, unused nulls, ignored array entries, whitespace,
// omissions, and a duplicate identical catalog identity. Marshal reorders keys.
func mutateCloudFixture(t *testing.T, family int, controls []byte) []byte {
	t.Helper()
	var mode, count byte
	if len(controls) > 0 {
		mode = controls[0]
	}
	if len(controls) > 1 {
		count = controls[1] % 4
	}
	f := metamorphicFixtures[family]
	root := metamorphicObject(t, []byte(f.body))
	decorate := func(v map[string]jsontext.Value) {
		if mode&1 != 0 {
			for n := 0; n <= int(count); n++ {
				v[fmt.Sprintf("future_%d", n)] = jsontext.Value(`{"nested":{"values":[null,{"unused":true}]}}`)
			}
		}
	}
	decorate(root)
	if f.name == "license" {
		// Standalone keys are region overrides, not unknown fields. Adding or
		// reordering across its ten-key boundary would change discovery meaning.
		if mode&2 != 0 {
			root["unused"] = jsontext.Value(`null`)
		}
	} else {
		data := metamorphicObject(t, root["data"])
		decorate(data)
		if mode&2 != 0 {
			root["msg"] = jsontext.Value(`null`)
			root["code"] = jsontext.Value(`null`)
			for _, key := range metamorphicOptionalFields[f.name] {
				data[key] = jsontext.Value(`null`)
			}
			if f.name == "detail" || f.name == "lan_detail" {
				data["location"] = jsontext.Value(`null`)
			}
		}
		if mode&32 != 0 {
			delete(root, "code")
			delete(root, "msg")
			for _, key := range metamorphicOptionalFields[f.name] {
				delete(data, key)
			}
		}
		if f.name == "devices" {
			var items []map[string]jsontext.Value
			if err := json.Unmarshal(data["items"], &items, json.MatchCaseInsensitiveNames(true)); err != nil {
				t.Fatal("invalid literal items")
			}
			for _, item := range items {
				decorate(item)
				location := metamorphicObject(t, item["location"])
				decorate(location)
				item["location"] = metamorphicJSON(t, location)
				if mode&2 != 0 {
					item["location"] = jsontext.Value(`null`)
				}
			}
			if mode&16 != 0 {
				items = append(items, items[0]) // Identical repeats have no new identity.
			}
			data["items"] = metamorphicJSON(t, items)
		}
		if raw, ok := data["location"]; ok && !bytes.Equal(raw, []byte(`null`)) {
			location := metamorphicObject(t, raw)
			decorate(location)
			data["location"] = metamorphicJSON(t, location)
		}
		if raw, ok := data["country"]; ok && !bytes.Equal(raw, []byte(`null`)) {
			country := metamorphicObject(t, raw)
			decorate(country)
			data["country"] = metamorphicJSON(t, country)
		}
		if mode&4 != 0 {
			entries := []jsontext.Value{jsontext.Value(`{"unused":null}`)}
			for n := 0; n <= int(count); n++ {
				entries = append(entries, jsontext.Value(`{"future":{"ignored":true}}`))
			}
			// These arrays have no consumed semantics. A distinct catalog
			// entry would change the projection and is deliberately not added.
			key := "futureEntries"
			if f.name == "profile" {
				key = "whitelistFunctions"
				entries = []jsontext.Value{jsontext.Value(`"example"`), jsontext.Value(`"future"`)}
			}
			data[key] = metamorphicJSON(t, entries)
		}
		root["data"] = metamorphicJSON(t, data)
	}
	b := metamorphicJSON(t, root)
	if mode&8 != 0 {
		out := jsontext.Value(bytes.Clone(b))
		if err := out.Indent(); err != nil {
			t.Fatal("cannot indent mutation")
		}
		b = append([]byte(" \r\n\t"), out...)
		b = append(b, '\n', '\t', ' ')
	}
	if len(b) > 8192 {
		t.Fatal("generator exceeded its body bound")
	}
	return b
}

func cloudProjection(name string, b []byte) (any, error) {
	switch name {
	case "login":
		token, err := cloud.ParseLogin(200, b, false)
		return token.Value(), err
	case "profile":
		return nil, cloud.ParseProfile(200, b)
	case "devices":
		// Compare selected identities, not the optional pagination hint.
		// Pagination and its local bounds have separate adapter tests.
		ds, _, err := cloud.ParseDevices(b)
		return ds, err
	case "detail":
		return cloud.ParseDetail(b)
	case "lan_detail":
		return cloud.ParseLANDetail(b)
	case "allocation":
		a, ids, err := cloud.ParseOnline(b)
		return struct {
			Allocation cloud.Allocation
			Cleanup    []cloud.Identifier
		}{a, ids}, err
	case "stop":
		return nil, cloud.ParseStop(b)
	case "license":
		return iotc.ParseLicenseJSON(b)
	}
	panic("unknown test family")
}

func checkCloudMetamorphism(t *testing.T, family int, controls []byte) {
	t.Helper()
	f := metamorphicFixtures[family]
	want, err := cloudProjection(f.name, []byte(f.body))
	if err != nil {
		t.Fatalf("%s: literal fixture rejected: %v", f.name, err)
	}
	b := mutateCloudFixture(t, family, controls)
	got, err := cloudProjection(f.name, b)
	if err != nil {
		t.Fatalf("%s: tolerable mutation rejected: %v", f.name, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: tolerable mutation changed consumed projection", f.name)
	}
}

func TestCloudMetamorphic(t *testing.T) {
	for family, f := range metamorphicFixtures {
		t.Run(f.name, func(t *testing.T) {
			for mode := byte(0); mode < 64; mode++ {
				for count := byte(0); count < 4; count++ {
					checkCloudMetamorphism(t, family, []byte{mode, count})
				}
			}
		})
	}
}

func FuzzCloudMetamorphic(f *testing.F) {
	// Seeds and source fixtures are literal; fuzz bytes control only bounded
	// transformations, never replace the known-valid source with arbitrary JSON.
	for family := range metamorphicFixtures {
		f.Add(uint8(family), []byte{0, 0})
		f.Add(uint8(family), []byte{63, 3})
	}
	f.Fuzz(func(t *testing.T, family uint8, controls []byte) {
		checkCloudMetamorphism(t, int(family)%len(metamorphicFixtures), controls)
	})
}

func TestCloudMetamorphicHTTP(t *testing.T) {
	for family, f := range metamorphicFixtures {
		if f.name != "devices" && f.name != "detail" && f.name != "lan_detail" && f.name != "allocation" {
			continue
		}
		for _, controls := range [][]byte{{0, 0}, {1, 2}, {2, 0}, {4, 3}, {8, 0}, {31, 3}} {
			t.Run(fmt.Sprintf("%s/%x", f.name, controls), func(t *testing.T) {
				body := mutateCloudFixture(t, family, controls)
				var logs bytes.Buffer
				c, err := newAuthenticatedFixture(t, Config{
					Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
					Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
						b := body
						if r.URL.Path == "/v1/my-device" && r.URL.Query().Get("current") != "1" {
							return nil, errors.New("unexpected catalog page")
						}
						if r.URL.Path == "/v1/device-p2p/stop" {
							b = []byte(`{"code":200}`)
						}
						return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b))}, nil
					}),
				})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				d := Device{Serial: "synthetic-serial-sentinel", Model: "S50 Pro"}
				switch f.name {
				case "devices":
					got, e := c.SelectDevice(t.Context(), d)
					if e != nil || got != d {
						t.Fatal("mutated catalog changed selection")
					}
				case "detail", "lan_detail":
					parse := c.Detail
					if f.name == "lan_detail" {
						parse = c.DetailForLAN
					}
					got, e := parse(t.Context(), d)
					want, _ := cloudProjection(f.name, []byte(f.body))
					if e != nil || !reflect.DeepEqual(got, want) {
						t.Fatal("mutated detail changed correlated projection")
					}
				case "allocation":
					lease, e := c.Acquire(t.Context(), d)
					if e != nil {
						t.Fatalf("mutated allocation rejected: %v", e)
					}
					want, _, _ := cloud.ParseOnline([]byte(f.body))
					if !reflect.DeepEqual(lease.Allocation(), want) {
						t.Fatal("mutated allocation changed lease")
					}
					if e := lease.Close(); e != nil {
						t.Fatal(e)
					}
				}
				if strings.Contains(logs.String(), "sentinel") || strings.Contains(logs.String(), "SYNTHETICUID") {
					t.Fatal("identity or secret entered logs")
				}
			})
		}
	}
}
