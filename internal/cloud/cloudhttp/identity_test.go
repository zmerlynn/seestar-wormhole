package cloudhttp

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestMismatchedDetailNeverAllocates(t *testing.T) {
	for _, field := range []string{"deviceSn", "deviceModel"} {
		t.Run(field, func(t *testing.T) {
			var details, allocs, stops atomic.Int32
			body := strings.Replace(detail, `"deviceSn":"serial"`, `"deviceSn":"wrong-serial"`, 1)
			if field == "deviceModel" {
				body = strings.Replace(detail, `"deviceModel":"S50 Pro"`, `"deviceModel":"wrong-model"`, 1)
			}
			c := setup(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "detail"):
					details.Add(1)
					io.WriteString(w, body)
				case strings.HasSuffix(r.URL.Path, "stop"):
					stops.Add(1)
					io.WriteString(w, `{"code":200,"data":null,"msg":"ok"}`)
				default:
					allocs.Add(1)
					io.WriteString(w, online)
				}
			})
			_, err := c.Detail(t.Context(), Device{Serial: "serial", Model: "S50 Pro"})
			if !errors.Is(err, cloud.ErrInvalid) || details.Load() != 1 || allocs.Load() != 0 || stops.Load() != 0 {
				t.Fatalf("mismatched %s: err=%v detail/allocation/stop=%d/%d/%d, want ErrInvalid, 1/0/0", field, err, details.Load(), allocs.Load(), stops.Load())
			}
		})
	}
}
