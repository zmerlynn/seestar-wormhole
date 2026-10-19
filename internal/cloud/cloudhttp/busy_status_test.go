package cloudhttp

import (
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestHTTP400VendorBusyDisposition(t *testing.T) {
	body := []byte(`{"code":300404,"data":null}`)
	_, _, busy := cloud.ParseOnline(body)
	for _, status := range []int{200, 400, 401, 403, 409, 503} {
		for _, complete := range []bool{false, true} {
			got := responseError(responseInfo{status: status, complete: complete}, body, busy, cloud.ErrTransport)
			want := cloud.Denied
			if status == 200 || (status == 400 && complete) {
				want = cloud.Busy
			}
			if cloud.Disposition(got) != want || !errors.Is(got, cloud.ErrTransport) {
				t.Errorf("status=%d complete=%v: rank=%v want %v with I/O cause retained", status, complete, cloud.Disposition(got), want)
			}
		}
	}
	// Unknown/malformed 400 responses must not become retryable.
	for _, cause := range []error{nil, cloud.ErrInvalid, cloud.ErrRejected} {
		if got := responseError(responseInfo{status: 400, complete: true}, nil, cause, nil); cloud.Disposition(got) < cloud.Denied {
			t.Errorf("400 cause=%v rank=%v want blocking denial", cause, cloud.Disposition(got))
		}
	}
}
