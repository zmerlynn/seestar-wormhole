package iotc

import (
	"reflect"
	"testing"
)

func TestLicenseStandaloneNonObjectLiterals(t *testing.T) {
	want, _ := ParseLicenseJSON([]byte(`{"realm":"test"}`))
	for _, body := range []string{
		`{"realm":"test","standalone":null}`, `{"realm":"test","standalone":[]}`,
		`{"realm":"test","standalone":"unused"}`, `{"realm":"test","standalone":false}`,
		`{"realm":"test","standalone":1}`,
	} {
		got, err := ParseLicenseJSON([]byte(body))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("non-object standalone changed discovery: %v", err)
		}
	}
}
