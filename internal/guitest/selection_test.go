//go:build guitest

package guitest

import (
	"errors"
	"strings"
	"testing"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud/cloudhttp"
)

func TestTelescopeScenarioCatalog(t *testing.T) {
	if !strings.Contains(","+UXScenarios+",", ",telescope-picker,") || !RequiresRelease("telescope-picker") || !RequiresRelease("dark-telescope-picker") {
		t.Fatal("picker scenario missing from campaign or release contract")
	}
	_, err := SelectTelescope(t.Context(), cloudhttp.Device{})
	var selection *cloudhttp.SelectionError
	if !errors.As(err, &selection) || !selection.Multiple {
		t.Fatal("fake account did not require selection")
	}
	choices := selection.Choices()
	if len(choices) != 2 || choices[0].Label != "Seestar S50 Pro synthetic-choice-1234" || choices[1].Label != `Future telescope\tmodel synthetic-choice-5678` {
		t.Fatalf("fixture choices=%+v", choices)
	}
	for _, choice := range choices {
		d, err := SelectTelescope(t.Context(), cloudhttp.Device{Serial: choice.Serial, Model: choice.Model})
		if err != nil || d.Serial != choice.Serial || d.Model != choice.Model {
			t.Fatal("fixture selection did not preserve pair", err)
		}
	}
}
