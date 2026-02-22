package main

import "github.com/spf13/cobra"

func newChannelsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "WhatsApp channel (newsletter) management",
	}
	cmd.AddCommand(newChannelsListCmd(flags))
	cmd.AddCommand(newChannelsInfoCmd(flags))
	cmd.AddCommand(newChannelsJoinCmd(flags))
	cmd.AddCommand(newChannelsLeaveCmd(flags))
	return cmd
}
