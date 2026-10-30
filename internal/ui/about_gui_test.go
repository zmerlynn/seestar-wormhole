//go:build gui

package ui

import (
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestNativeAboutContent(t *testing.T) {
	if got := aboutDescription("synthetic-version"); got != "Version synthetic-version\nThe Unlicense (public domain)\nhttps://github.com/zmerlynn/seestar-wormhole/releases" {
		t.Fatal(got)
	}
}

func TestNativeProjectMenuPreservesRoles(t *testing.T) {
	for _, withAppMenu := range []bool{false, true} {
		menu := application.NewMenu()
		menu.AddRole(application.HelpMenu)
		for _, role := range []application.Role{application.Quit, application.CloseWindow, application.Copy, application.Paste} {
			menu.Append(application.NewMenuFromItems(application.NewMenuItem("native role").SetRole(role)))
		}
		if withAppMenu {
			submenu := application.NewMenuFromItems(application.NewMenuItem("system About").SetRole(application.About))
			menu.Prepend(application.NewMenuFromItems(application.NewSubmenu("seestar-wormhole", submenu).SetRole(application.AppMenu)))
		}
		configureProjectMenu(menu, func() { t.Error("About opened during setup") }, func(string) error { t.Error("browser opened during setup"); return nil })
		if menu.FindByRole(application.About) != nil || menu.FindByLabel("About seestar-wormhole") == nil || menu.FindByLabel("Project releases") == nil || menu.FindByLabel("Learn More") != nil {
			t.Fatal("project menu missing or still uses the system About role")
		}
		for _, role := range []application.Role{application.Quit, application.CloseWindow, application.Copy, application.Paste} {
			if menu.FindByRole(role) == nil {
				t.Fatalf("lost native role %v", role)
			}
		}
		parent := menu.FindByRole(application.HelpMenu)
		if withAppMenu {
			parent = menu.FindByRole(application.AppMenu)
		}
		if parent.GetSubmenu().FindByLabel("About seestar-wormhole") == nil {
			t.Fatal("About is in the wrong menu")
		}
	}
}

func TestNativePageLinksUseFixedDestinations(t *testing.T) {
	for _, url := range []string{releasesURL, licenseURL, portPermissionsURL, copyingURL} {
		var got string
		openPageLink("seestar:open:"+url, func(s string) error { got = s; return nil })
		if got != url {
			t.Fatalf("link lost: %s", url)
		}
		html, _ := assets.ReadFile("web/index.html")
		if !strings.Contains(string(html), `href="`+url+`" target="_blank" rel="noreferrer"`) {
			t.Fatalf("missing external link: %s", url)
		}
	}
	for _, message := range []string{"https://example.invalid/", "seestar:open:https://example.invalid/", "seestar:open:" + releasesURL + "?extra", "seestar:open:file:///synthetic", "seestar:open:https://github.com/zmerlynn/seestar-wormhole/blob/main/docs/copying.md"} {
		openPageLink(message, func(string) error { t.Fatalf("unexpected external destination: %q", message); return nil })
	}
}
