package cloud

import (
	"errors"
	"strings"
	"testing"
)

func TestOfflineEnvelopeClassification(t *testing.T) {
	// Code 300406 denotes an offline device, distinct from status 2.
	b := []byte(`{"code":300406,"data":null,"msg":"account-secret"}`)
	_, _, onlineErr := ParseOnline(b)
	_, detailErr := ParseDetail(b)
	for name, err := range map[string]error{"online": onlineErr, "detail": detailErr, "stop": ParseStop(b)} {
		if !errors.Is(err, ErrOffline) {
			t.Errorf("%s: got %v, want ErrOffline", name, err)
		}
	}
}

func TestUnknownOnlineFieldPreservesCleanup(t *testing.T) {
	b := strings.Replace(onlineFixture, `"p2pStatus":1`, `"p2pStatus":1,"unknown":true`, 1)
	a, ids, err := ParseOnline([]byte(b))
	if err != nil || a.ConnectionID() != "allocation-secret" {
		t.Errorf("unknown allocation field: got %v, want valid allocation", err)
	}
	if len(ids) != 1 || ids[0].Value() != "allocation-secret" {
		t.Fatal("unknown field lost recovered cleanup identifier")
	}
}
