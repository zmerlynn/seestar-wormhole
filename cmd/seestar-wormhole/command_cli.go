//go:build !gui

package main

import "github.com/spf13/cobra"

const guiEnabled = false
const programName = "seestar-wormhole-cli"

func addUICommand(*cobra.Command, *options) {}
