package cloudhttp

import (
	"errors"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudRequiredRecordLiterals(t *testing.T) {
	// Each record is literal and changes only one presence prerequisite.
	for _, record := range []string{
		`{"deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-ssid-sentinel"}`,
		`{"deviceSn":null,"deviceModel":"S50 Pro","deviceStatus":1,"ssid":"synthetic-ssid-sentinel"}`,
		`{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-ssid-sentinel"}`,
		`{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":null,"ssid":"synthetic-ssid-sentinel"}`,
		`{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":1}`,
		`{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","deviceStatus":1,"ssid":null}`,
	} {
		for _, family := range []string{"devices", "detail", "lan_detail"} {
			body := `{"code":200,"data":` + record + `}`
			if family == "devices" {
				body = `{"code":200,"data":{"items":[` + record + `]}}`
			}
			_, err := cloudProjection(family, []byte(body))
			if !errors.Is(err, cloud.ErrInvalid) {
				t.Errorf("%s: missing/null prerequisite got %v, want invalid", family, err)
			}
			assertCloudSecretSafe(t, err, "")
		}
	}
}

func TestCloudCatalogIntegerStatusLiteral(t *testing.T) {
	const body = `{"code":200,"data":{"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-ssid-sentinel","deviceStatus":"occupied"}]}}`
	_, _, err := cloud.ParseDevices([]byte(body))
	if !errors.Is(err, cloud.ErrInvalid) {
		t.Fatalf("non-integer catalog status: got %v, want invalid", err)
	}
	assertCloudSecretSafe(t, err, "")
	if strings.Contains(err.Error(), "occupied") {
		t.Fatal("raw status echoed in error")
	}
}
