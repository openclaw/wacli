package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsCommunityCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "community",
		Short: "Manage the groups of a community",
	}
	cmd.AddCommand(newGroupsCommunityGroupsCmd(flags))
	cmd.AddCommand(newGroupsCommunityLinkCmd(flags, "link", "Move an existing group into a community"))
	cmd.AddCommand(newGroupsCommunityLinkCmd(flags, "unlink", "Take a group out of a community (the group stays)"))
	return cmd
}

type communityGroupEntry struct {
	JID          string `json:"jid"`
	Name         string `json:"name"`
	DefaultGroup bool   `json:"default_group"`
}

func communityGroupEntries(targets []*types.GroupLinkTarget) []communityGroupEntry {
	entries := make([]communityGroupEntry, 0, len(targets))
	for _, t := range targets {
		if t == nil {
			continue
		}
		entries = append(entries, communityGroupEntry{JID: t.JID.String(), Name: t.Name, DefaultGroup: t.IsDefaultSubGroup})
	}
	return entries
}

func newGroupsCommunityGroupsCmd(flags *rootFlags) *cobra.Command {
	var communityStr string
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "List a community's groups (live)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(communityStr) == "" {
				return fmt.Errorf("--community is required")
			}
			community, err := parseGroupJID(communityStr)
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
			targets, err := a.WA().GetSubGroups(ctx, community)
			if err != nil {
				return err
			}
			entries := communityGroupEntries(targets)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, entries)
			}
			for _, e := range entries {
				fmt.Fprintf(os.Stdout, "%s\t%s\n", e.JID, sanitize(e.Name))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&communityStr, "community", "", "community JID (…@g.us)")
	return cmd
}

func newGroupsCommunityLinkCmd(flags *rootFlags, action, short string) *cobra.Command {
	var communityStr string
	var jidStr string
	cmd := &cobra.Command{
		Use:   action,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(communityStr) == "" || strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--community and --jid are required")
			}
			community, err := parseGroupJID(communityStr)
			if err != nil {
				return err
			}
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
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
			if action == "link" {
				err = a.WA().LinkGroup(ctx, community, gjid)
			} else {
				err = a.WA().UnlinkGroup(ctx, community, gjid)
			}
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"community": community.String(), "jid": gjid.String(), "linked": action == "link"})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&communityStr, "community", "", "community JID (…@g.us)")
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}
