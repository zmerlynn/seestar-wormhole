package cloud

import "testing"

// Synthetic wire literals and explicit pair order exercise local client
// deduplication policy, not a claim that the service deduplicates records.
func TestCatalogPairLiterals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       [][2]string
	}{
		{"same serial different models", `{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":2,"ssid":"first"},{"deviceSn":"synthetic-a","deviceModel":"S30","deviceStatus":1,"ssid":"second"}]}}`, [][2]string{{"synthetic-a", "S50"}, {"synthetic-a", "S30"}}},
		{"model comparison is exact", `{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":1,"ssid":"first"},{"deviceSn":"synthetic-a","deviceModel":"s50","deviceStatus":4,"ssid":"second"}]}}`, [][2]string{{"synthetic-a", "S50"}, {"synthetic-a", "s50"}}},
		{"duplicate first pair around distinct model", `{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":2,"ssid":"first"},{"deviceSn":"synthetic-a","deviceModel":"S30","deviceStatus":1,"ssid":"second"},{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":4,"ssid":"third"}]}}`, [][2]string{{"synthetic-a", "S50"}, {"synthetic-a", "S30"}}},
		{"different serial same model", `{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":4,"ssid":"first"},{"deviceSn":"synthetic-b","deviceModel":"S50","deviceStatus":2,"ssid":"second"}]}}`, [][2]string{{"synthetic-a", "S50"}, {"synthetic-b", "S50"}}},
		{"exact duplicate with disagreeing status", `{"data":{"items":[{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":2,"ssid":"first"},{"deviceSn":"synthetic-a","deviceModel":"S50","deviceStatus":1,"ssid":"second"}]}}`, [][2]string{{"synthetic-a", "S50"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := ParseDevices([]byte(tc.body))
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("count=%d error=%v, want %d pairs", len(got), err, len(tc.want))
			}
			for i, want := range tc.want {
				if got[i].Serial() != want[0] || got[i].Model() != want[1] {
					t.Errorf("pair[%d] differs from literal expectation", i)
				}
			}
		})
	}
}
