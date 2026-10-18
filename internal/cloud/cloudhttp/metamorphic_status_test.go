package cloudhttp

import (
	"errors"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudIntegerStatusLiteral(t *testing.T) {
	const body = `{"code":200,"data":{"deviceSn":"synthetic-serial-sentinel","deviceModel":"S50 Pro","ssid":"synthetic-ssid-sentinel","deviceStatus":"occupied"}}`
	for _, parse := range []func([]byte) (cloud.Detail, error){cloud.ParseDetail, cloud.ParseLANDetail} {
		_, err := parse([]byte(body))
		if !errors.Is(err, cloud.ErrInvalid) {
			t.Fatalf("non-integer status: got %v, want invalid", err)
		}
		assertCloudSecretSafe(t, err, "")
		if strings.Contains(err.Error(), "occupied") {
			t.Fatal("raw status echoed in error")
		}
	}
}
