//go:build gui

package ui

import "github.com/wailsapp/wails/v3/pkg/application"

const releasesURL = "https://github.com/zmerlynn/seestar-wormhole/releases"
const licenseURL = "https://unlicense.org/"
const portPermissionsURL = "https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/phone-app.md#port-80-and-permissions"
const copyingURL = "https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/copying.md#copy-with-a-graphical-app"

func aboutDescription(version string) string {
	return "Version " + version + "\nThe Unlicense (public domain)\n" + releasesURL
}

func configureProjectMenu(menu *application.Menu, showAbout func(), openURL func(string) error) {
	about := application.NewMenuItem("About seestar-wormhole").OnClick(func(*application.Context) { showAbout() })
	if appMenu := menu.FindByRole(application.AppMenu); appMenu != nil {
		submenu := appMenu.GetSubmenu()
		submenu.RemoveMenuItem(submenu.FindByRole(application.About))
		submenu.Prepend(application.NewMenuFromItems(about))
	}
	help := menu.FindByRole(application.HelpMenu).GetSubmenu()
	help.Clear()
	help.Add("Project releases").OnClick(func(*application.Context) { _ = openURL(releasesURL) })
	if menu.FindByRole(application.AppMenu) == nil {
		help.AddSeparator()
		help.Append(application.NewMenuFromItems(about))
	}
}

// Only the page's fixed destinations can cross the raw-message bridge. The
// loopback page does not use Wails' separate HTTP runtime transport.
func openPageLink(message string, openURL func(string) error) {
	for _, url := range []string{releasesURL, licenseURL, portPermissionsURL, copyingURL} {
		if message == "seestar:open:"+url {
			_ = openURL(url)
			return
		}
	}
}
