package iotc

import (
	"strings"
	"testing"
)

// Validate retained standalone entries.
func TestLicenseStandaloneValidation(t *testing.T) {
	for _, input := range []string{`{"realm":"test","standalone":{"":"bad.invalid"}}`, `{"realm":"test","standalone":{"z":null}}`, `{"realm":"test","standalone":{"z":1}}`, `{"realm":"test","standalone":{"` + strings.Repeat("r", 129) + `":"host.invalid"}}`, `{"realm":"test","standalone":{"z":"` + strings.Repeat("h", 129) + `"}}`} {
		if _, err := ParseLicenseJSON([]byte(input)); err == nil {
			t.Errorf("accepted invalid retained entry: %s", input)
		}
	}
	l, err := ParseLicenseJSON([]byte(`{"realm":"test","nebulaDomainFromLicense":true,"standalone":{"z":"host.invalid"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := l.MasterDomain("all"); got != "all-c-master-test.iotcplatform.com" {
		t.Errorf("Nebula changed Seestar discovery: %q", got)
	}
	for _, n := range []int{127, 128} {
		if _, err := ParseLicenseJSON([]byte(`{"realm":"test","standalone":{"` + strings.Repeat("r", n) + `":"` + strings.Repeat("h", n) + `"}}`)); err != nil {
			t.Errorf("%d-character slot rejected: %v", n, err)
		}
	}
}

// Region names are exact keys, not DNS labels; only the selected host needs
// DNS syntax validation. Standalone region lookup is case sensitive.
func TestLicenseStandaloneExactRegion(t *testing.T) {
	l, err := ParseLicenseJSON([]byte(`{"realm":"test","standalone":{"South Pole":"first.invalid","south pole":"second.invalid"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ region, host string }{{"South Pole", "first.invalid"}, {"south pole", "second.invalid"}} {
		if got, err := l.MasterDomain(tc.region); err != nil || got != tc.host {
			t.Errorf("region %q = %q, %v; want %q", tc.region, got, err, tc.host)
		}
	}
}

func TestLicenseOverridesSortedCap(t *testing.T) {
	l, err := ParseLicenseJSON([]byte(`{"realm":"test","standalone":{"z":null,"j":"j.invalid","i":"i.invalid","h":"h.invalid","g":"g.invalid","f":"f.invalid","e":"e.invalid","d":"d.invalid","c":"c.invalid","b":"b.invalid","a":"a.invalid"}}`))
	if err != nil || len(l.Standalone) != 10 || l.StandaloneOmitted != 1 {
		t.Fatalf("sorted cap: %+v, %v", l, err)
	}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		if l.Standalone[name] != name+".invalid" {
			t.Errorf("entry %s = %q", name, l.Standalone[name])
		}
	}
}
