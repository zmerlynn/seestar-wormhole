package cloud

import "testing"

func TestOnlineCleanupAfterEarlierTypeError(t *testing.T) {
	for _, frame := range []string{
		`{"code":"bad","data":{"conId":"cleanup","deviceUid":"uid","sdkLicenseKey":"license","username":"user","password":"pass"}}`,
		`{"data":{"password":{},"conId":"cleanup","deviceUid":"uid","sdkLicenseKey":"license","username":"user"}}`,
	} {
		a, ids, err := ParseOnline([]byte(frame))
		if err == nil || a.ConnectionID() != "" || len(ids) != 1 || ids[0].Value() != "cleanup" {
			t.Fatalf("lost cleanup: %v %v %v", a, ids, err)
		}
	}
}
