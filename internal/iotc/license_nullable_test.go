package iotc

import (
	"reflect"
	"testing"
)

func TestLicenseNullableFlagLiterals(t *testing.T) {
	// Absent, null and non-boolean flags mean false, never truthy.
	want, err := ParseLicenseJSON([]byte(`{"realm":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"realm":"test","nebulaDomainFromLicense":null}`,
		`{"realm":"test","nebulaDomainFromLicense":"true"}`,
		`{"realm":"test","nebulaDomainFromLicense":1}`,
		`{"realm":"test","nebulaDomainFromLicense":[]}`,
		`{"realm":"test","nebulaDomainFromLicense":{}}`,
	} {
		got, err := ParseLicenseJSON([]byte(body))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("non-boolean license flag changed discovery: %v", err)
		}
	}
	got, err := ParseLicenseJSON([]byte(`{"realm":"test","nebulaDomainFromLicense":true}`))
	if err != nil || !got.NebulaDomainFromLicense {
		t.Fatal("boolean true flag lost")
	}
}
