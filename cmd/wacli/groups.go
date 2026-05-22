package main

import "github.com/spf13/cobra"

func newGroupsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Group management",
	}
	cmd.AddCommand(newGroupsCreateCmd(flags))
	cmd.AddCommand(newGroupsListCmd(flags))
	cmd.AddCommand(newGroupsRefreshCmd(flags))
	cmd.AddCommand(newGroupsInfoCmd(flags))
	cmd.AddCommand(newGroupsRenameCmd(flags))
	cmd.AddCommand(newGroupsTopicCmd(flags, "topic"))
	cmd.AddCommand(newGroupsTopicCmd(flags, "description"))
	cmd.AddCommand(newGroupsAnnounceOnlyCmd(flags))
	cmd.AddCommand(newGroupsLockedCmd(flags))
	cmd.AddCommand(newGroupsParticipantsCmd(flags))
	cmd.AddCommand(newGroupsRequestsCmd(flags))
	cmd.AddCommand(newGroupsInviteCmd(flags))
	cmd.AddCommand(newGroupsJoinCmd(flags))
	cmd.AddCommand(newGroupsLeaveCmd(flags))
	cmd.AddCommand(newGroupsPruneCmd(flags))
	return cmd
}
