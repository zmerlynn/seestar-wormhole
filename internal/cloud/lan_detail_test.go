package cloud

import "testing"

func TestLANDetailRetainsName(t *testing.T) {
	for _, name := range []string{`"Scope Wi-Fi"`, `""`} {
		d, err := ParseLANDetail([]byte(`{"code":200,"data":{"deviceSn":"real-sn","deviceModel":"S50 Pro","deviceStatus":1,"ssid":` + name + `,"deviceAlias":"different alias"}}`))
		if err != nil {
			t.Fatal(err)
		}
		want := "Scope Wi-Fi"
		if name == `""` {
			want = ""
		}
		if d.Name() != want || d.Serial() != "real-sn" || d.Model() != "S50 Pro" {
			t.Fatalf("identity/name lost: name %q", d.Name())
		}
	}
}

func TestDeviceSSIDIsTypedInBothDetailPaths(t *testing.T) {
	b := []byte(`{"code":200,"data":{"deviceSn":"SN","deviceModel":"S50","deviceStatus":1,"ssid":false}}`)
	if _, err := ParseDetail(b); err == nil {
		t.Fatal("mistyped required SSID accepted")
	}
	if _, err := ParseLANDetail(b); err == nil {
		t.Fatal("invalid consumed LAN name accepted")
	}
}
