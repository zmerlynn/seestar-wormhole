package iotc

import "testing"

func TestLicenseDuplicateNames(t *testing.T) {
	for _, frame := range []string{
		`{"realm":"test","realm":"test"}`,
		`{"realm":"test","rea\u006cm":"test"}`,
		`{"realm":"test","standalone":{"all":"first.invalid","all":"last.invalid"}}`,
		`{"realm":"test","future":{"a":1,"a":2}}`,
	} {
		if _, err := ParseLicenseJSON([]byte(frame)); err == nil {
			t.Fatalf("accepted duplicate: %s", frame)
		}
	}
	l, err := ParseLicenseJSON([]byte(`{"realm":"test","REALM":"other","KEYMASK":"invalid","keyMask":null}`))
	if err != nil || l.Realm != "test" || l.Mask != [16]byte{} {
		t.Fatalf("case matching: %+v, %v", l, err)
	}
}

func FuzzLicenseJSON(f *testing.F) {
	for _, frame := range []string{
		`{"realm":"test"}`,
		`{"realm":"test","realm":"other"}`,
		`{"realm":"test","standalone":{"all":"first.invalid","all":"last.invalid"}}`,
		`{"realm":"test","future":{"a":1,"a":2}}`,
	} {
		f.Add([]byte(frame))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		l, err := ParseLicenseJSON(b)
		if err == nil && (len(l.Standalone) > 10 || l.StandaloneOmitted < 0) {
			t.Fatal("invalid accepted license")
		}
	})
}
