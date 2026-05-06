package main

import "github.com/spf13/cobra"

func newStoreCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "store",
		Short: "Manage local data store",
	}
	cmd.AddCommand(newStoreCleanupCmd(flags))
	cmd.AddCommand(newStoreStatsCmd(flags))
	return cmd
}
