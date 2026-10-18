package cloudhttp

import (
	"errors"
	"github.com/go-json-experiment/json/jsontext"
	"reflect"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudOptionalPresence(t *testing.T) {
	// Check optional-member presence/nullability independently; generated
	// combinations supplement these comparisons.
	for _, f := range metamorphicFixtures {
		if f.name == "license" {
			continue
		}
		want, err := cloudProjection(f.name, []byte(f.body))
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range append([]string{"code", "msg"}, metamorphicOptionalFields[f.name]...) {
			for _, absent := range []bool{false, true} {
				t.Run(f.name+"/"+field+"/"+map[bool]string{true: "absent", false: "null"}[absent], func(t *testing.T) {
					root := metamorphicObject(t, []byte(f.body))
					obj := root
					if field != "code" && field != "msg" {
						obj = metamorphicObject(t, root["data"])
					}
					if absent {
						delete(obj, field)
					} else {
						obj[field] = jsontext.Value(`null`)
					}
					if field != "code" && field != "msg" {
						root["data"] = metamorphicJSON(t, obj)
					}
					got, err := cloudProjection(f.name, metamorphicJSON(t, root))
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("absent/null optional field changed projection: %v", err)
					}
				})
			}
		}
	}
}

func TestCloudLicenseOptionalLiterals(t *testing.T) {
	want, err := cloudProjection("license", []byte(`{"realm":"example"}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"realm":"example","keyMask":null}`,
		`{"realm":"example","standalone":null}`,
		`{"realm":"example","nebulaDomainFromLicense":null}`,
		`{"realm":"example","nebulaDomainFromLicense":"true"}`,
		`{"realm":"example","keyMask":null,"standalone":null,"nebulaDomainFromLicense":false}`,
	} {
		got, err := cloudProjection("license", []byte(body))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("null license option changed omitted meaning: %v", err)
		}
	}
	for _, body := range []string{
		`{"realm":null}`,
		`{"realm":"example","keyMask":1}`,
		`{"realm":"example","keyMask":{}}`,
		`{"realm":"example","keyMask":[]}`,
		`{"realm":"example","keyMask":true}`,
		`{"realm":"example","standalone":{"all":null}}`,
	} {
		_, err := cloudProjection("license", []byte(body))
		if err == nil {
			t.Fatal("invalid license prerequisite accepted")
		}
		assertCloudSecretSafe(t, err, "")
	}
}

func TestCloudAllocationDataRequiredLiterals(t *testing.T) {
	for _, body := range []string{`{"code":200}`, `{"code":200,"data":null}`} {
		_, _, err := cloud.ParseOnline([]byte(body))
		if !errors.Is(err, cloud.ErrInvalid) {
			t.Fatalf("missing/null allocation: %v", err)
		}
		assertCloudSecretSafe(t, err, "")
	}
}
