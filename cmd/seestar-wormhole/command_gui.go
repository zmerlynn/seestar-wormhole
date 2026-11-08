//go:build gui

package main

import "github.com/spf13/cobra"

const guiEnabled = true
const programName = "seestar-wormhole"

func addUICommand(root *cobra.Command, o *options) { root.AddCommand(newUICommand(o)) }
