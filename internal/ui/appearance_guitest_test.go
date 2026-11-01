//go:build gui && guitest

package ui

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestFixtureNativeAppearance(t *testing.T) {
	for _, scenario := range []string{"fresh", "relaunch", "dark", "dark-relaunch", "dark-telescope-picker"} {
		want := application.NSAppearanceNameAqua
		if scenario == "dark" || scenario == "dark-relaunch" || scenario == "dark-telescope-picker" {
			want = application.NSAppearanceNameDarkAqua
		}
		options := guiTestWindowOptions(scenario)
		if options.Mac.Appearance != want || !options.AllowSimpleEventEmit {
			t.Fatalf("%s: native appearance or probe bridge missing", scenario)
		}
	}
}
