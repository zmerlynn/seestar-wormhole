package iotc

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLicenseElevenLiteral(t *testing.T) {
	b, err := os.ReadFile("testdata/license-eleven.json")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a": "a.invalid", "b": "b.invalid", "c": "c.invalid", "d": "d.invalid", "e": "e.invalid", "f": "f.invalid", "g": "g.invalid", "h": "h.invalid", "i": "i.invalid", "j": "j.invalid"}
	// Reversing document order keeps the same sorted entries.
	reverse := []byte(`{"realm":"test","standalone":{"k":"k.invalid","j":"j.invalid","i":"i.invalid","h":"h.invalid","g":"g.invalid","f":"f.invalid","e":"e.invalid","d":"d.invalid","c":"c.invalid","b":"b.invalid","a":"a.invalid"},"unused":{"future":true}}`)
	for _, input := range [][]byte{b, reverse} {
		for range 64 {
			l, err := ParseLicenseJSON(input)
			if err != nil {
				t.Fatalf("eleven valid standalone entries rejected: %v", err)
			}
			if !reflect.DeepEqual(l.Standalone, want) {
				t.Fatalf("retained %v, want %v", l.Standalone, want)
			}
			for _, tc := range []struct{ region, want string }{
				{"a", "a.invalid"}, {"j", "j.invalid"}, {"k", "k-c-master-test.iotcplatform.com"}, {"", "all-c-master-test.iotcplatform.com"}, {"cn", "cn-c-master-test.kalay.net.cn"},
			} {
				got, err := l.MasterDomain(tc.region)
				if err != nil || got != tc.want {
					t.Fatalf("region %q: got %q, %v; want %q", tc.region, got, err, tc.want)
				}
				c := config()
				c.License = l
				c.Region = tc.region
				m, err := New(c)
				if err != nil || m.domain != tc.want {
					t.Fatalf("New region %q: machine %v, error %v", tc.region, m, err)
				}
			}
		}
	}
}

func TestLicenseRetentionSafety(t *testing.T) {
	b, err := os.ReadFile("testdata/license-eleven.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, old, replacement string }{

		{"retained domain", `"a.invalid"`, `"https://a.invalid"`},
		{"long region", `"a":`, `"` + strings.Repeat("a", 254) + `":`},
		{"realm type", `"realm":"test"`, `"realm":true`},
		{"mask type", `"realm":"test"`, `"realm":"test","keyMask":1`},
		{"mask length", `"realm":"test"`, `"realm":"test","keyMask":"AA=="`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := bytes.Replace(b, []byte(tc.old), []byte(tc.replacement), 1)
			if _, err := ParseLicenseJSON(bad); err == nil {
				t.Fatal("accepted invalid discovery field")
			}
		})
	}
	for _, size := range []int{16384, 16385} {
		padded := append(bytes.Clone(b), bytes.Repeat([]byte(" "), size-len(b))...)
		_, err := ParseLicenseJSON(padded)
		if (err == nil) != (size == 16384) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	for _, bad := range [][]byte{nil, append(bytes.Clone(b), []byte(`{}`)...), b[:len(b)-1]} {
		if _, err := ParseLicenseJSON(bad); err == nil {
			t.Fatal("accepted empty, multiple or truncated document")
		}
	}
}

func TestLicenseRetentionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		input        string
		count        int
		region, want string
	}{
		{`{"realm":"test"}`, 0, "", "all-c-master-test.iotcplatform.com"},
		{`{"realm":"test","standalone":null,"keyMask":null,"nebulaDomainFromLicense":null}`, 0, "cn", "cn-c-master-test.kalay.net.cn"},
		{`{"realm":"test","standalone":{"a":"a.invalid","b":"b.invalid","c":"c.invalid","d":"d.invalid","e":"e.invalid","f":"f.invalid","g":"g.invalid","h":"h.invalid","i":"i.invalid","j":"j.invalid"}}`, 10, "j", "j.invalid"},
		{`{"realm":"test","standalone":{"z":"z.invalid","j":"j.invalid","i":"i.invalid","h":"h.invalid","g":"g.invalid","f":"f.invalid","e":"e.invalid","d":"d.invalid","cn":"china.invalid","b":"b.invalid","all":"global.invalid"}}`, 10, "", "global.invalid"},
		{`{"realm":"test","standalone":{"z":"z.invalid","j":"j.invalid","i":"i.invalid","h":"h.invalid","g":"g.invalid","f":"f.invalid","e":"e.invalid","d":"d.invalid","cn":"china.invalid","b":"b.invalid","all":"global.invalid"}}`, 10, "cn", "china.invalid"},
	} {
		l, err := ParseLicenseJSON([]byte(tc.input))
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Standalone) != tc.count {
			t.Fatalf("retained %d; want %d", len(l.Standalone), tc.count)
		}
		got, err := l.MasterDomain(tc.region)
		if err != nil || got != tc.want {
			t.Fatalf("region %q: got %q, %v; want %q", tc.region, got, err, tc.want)
		}
	}
}
