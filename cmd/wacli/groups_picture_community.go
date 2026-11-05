package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
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
	gjid, err := parseGroupJID(jidStr)
	if err != nil {
		return err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	pictureID, err := a.WA().SetGroupPhoto(ctx, gjid, imgBytes)
	if err != nil {
		return fmt.Errorf("set group picture: %w", err)
	}
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), "picture_id": pictureID})
	}
	if imgBytes == nil {
		fmt.Fprintln(os.Stdout, "Group picture removed")
		return nil
	}
	fmt.Fprintf(os.Stdout, "Group picture updated (id: %s)\n", pictureID)
	return nil
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
			child, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			parent, err := parseGroupJID(parentStr)
			if err != nil {
				return fmt.Errorf("--parent: %w", err)
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			if link {
				err = a.WA().LinkGroup(ctx, parent, child, hidden)
			} else {
				err = a.WA().UnlinkGroup(ctx, parent, child)
			}
			if err != nil {
				return fmt.Errorf("%s group: %w", use, err)
			}
			if info, err := a.WA().GetGroupInfo(ctx, child); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				res := map[string]any{"jid": child.String(), "parent": parent.String(), "linked": link}
				if link {
					res["hidden"] = hidden
				}
				return out.WriteJSON(os.Stdout, res)
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringVar(&parentStr, "parent", "", "community parent group JID (…@g.us)")
	if link {
		cmd.Flags().BoolVar(&hidden, "hidden", false, "hide the group from the community's group list (fixed at link time)")
	}
	return cmd
}
