package cloudhttp

import (
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudCatalogDuplicatePolicyLiterals(t *testing.T) {
	// Pair deduplication is local client policy; models distinguish pairs.
	for _, tc := range []struct {
		body  string
		count int
		want  error
	}{
		{`{"code":200,"data":{"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"one","deviceStatus":1},{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"two","deviceStatus":4}]}}`, 1, nil},
		{`{"code":200,"data":{"items":[{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"one","deviceStatus":1},{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S30","ssid":"two","deviceStatus":4}]}}`, 2, nil},
	} {
		got, _, err := cloud.ParseDevices([]byte(tc.body))
		if !errors.Is(err, tc.want) || len(got) != tc.count {
			t.Fatalf("duplicate policy: count=%d err=%v", len(got), err)
		}
		if err == nil && (got[0].Serial() != "synthetic-serial-sentinel" || got[0].Model() != "S50 Pro") {
			t.Fatal("duplicate changed identity")
		}
		assertCloudSecretSafe(t, err, "")
	}
}
