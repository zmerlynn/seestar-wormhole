package proxy

import (
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
	"testing"
)

func TestCloudConfigurationTerminalProjection(t *testing.T) {
	err := cloudTerminal(cloud.ErrConfig)
	_, reason := diagnostic.Fields(err, "")
	if reason != "invalid_configuration" || !errors.Is(err, session.ErrUncertain) || errors.Is(err, cloud.ErrConfig) {
		t.Fatalf("normalized configuration: reason=%s uncertain=%t config=%t", reason, errors.Is(err, session.ErrUncertain), errors.Is(err, cloud.ErrConfig))
	}
}
