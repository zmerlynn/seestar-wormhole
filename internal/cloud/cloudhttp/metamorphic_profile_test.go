package cloudhttp

import (
	"errors"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
)

func TestCloudProfileIDRequiredLiterals(t *testing.T) {
	for _, body := range []string{
		`{"code":200,"data":{}}`,
		`{"code":200,"data":{"id":null,"country":null,"whitelistFunctions":null}}`,
	} {
		err := cloud.ParseProfile(200, []byte(body))
		if !errors.Is(err, cloud.ErrInvalid) {
			t.Errorf("profile without ID: got %v, want invalid", err)
		}
		assertCloudSecretSafe(t, err, "")
	}
}
