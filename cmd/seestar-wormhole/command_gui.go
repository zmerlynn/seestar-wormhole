//go:build gui

package main

import "github.com/spf13/cobra"

const guiEnabled = true
const programName = "seestar-wormhole"

func addUICommand(root *cobra.Command, o *options) {
	command := &cobra.Command{Use: "ui", Short: "Open local controls in a native window", Args: noArguments, RunE: func(*cobra.Command, []string) error { return nil }}
	command.Flags().StringVar(&o.stateDir, "state-dir", "", "private UI settings and shared installation identity directory")
	root.AddCommand(command)
}
