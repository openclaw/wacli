package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newGroupsSetPictureCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "set-picture <image>",
		Short: "Set a group's picture (JPEG or PNG, auto-resized to <=640px)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			imgBytes, err := readAsJPEG(args[0])
			if err != nil {
				return fmt.Errorf("read image: %w", err)
			}
			return runGroupPicture(flags, gjid.String(), imgBytes)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsRemovePictureCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "remove-picture",
		Short: "Remove a group's picture",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			return runGroupPicture(flags, gjid.String(), nil)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func runGroupPicture(flags *rootFlags, jidStr string, imgBytes []byte) error {
	return runGroupCommand(flags, groupOp{Action: groupActionPicture, JID: jidStr, Image: imgBytes}, true)
}

// newGroupsLinkCmd builds `groups link` (link=true) or `groups unlink` (link=false):
// add an existing group to a community, or take it out.
func newGroupsLinkCmd(flags *rootFlags, link bool) *cobra.Command {
	var jidStr, parentStr string
	var hidden bool
	use, short := "link", "Add an existing group to a community"
	if !link {
		use, short = "unlink", "Remove a group from its community"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(parentStr) == "" {
				return fmt.Errorf("--parent is required (community parent group JID)")
			}
			if _, err := parseGroupJID(jidStr); err != nil {
				return err
			}
			if _, err := parseGroupJID(parentStr); err != nil {
				return fmt.Errorf("--parent: %w", err)
			}
			action := groupActionLink
			if !link {
				action = groupActionUnlink
			}
			return runGroupCommand(flags, groupOp{Action: action, JID: jidStr, Parent: parentStr, Hidden: hidden}, true)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringVar(&parentStr, "parent", "", "community parent group JID (…@g.us)")
	if link {
		cmd.Flags().BoolVar(&hidden, "hidden", false, "hide the group from the community's group list (fixed at link time)")
	}
	return cmd
}
