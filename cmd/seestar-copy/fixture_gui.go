//go:build gui && !guitest

package main

import (
	"context"
	"io"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// fixture is the native check's driver, which only a guitest build has.
type fixture struct{}

func newFixture(context.Context, func(), io.Writer) (*fixture, error) { return nil, nil }

func (*fixture) configure(*application.Options) {}

func (*fixture) attach(*application.App, *application.WebviewWindow) {}
