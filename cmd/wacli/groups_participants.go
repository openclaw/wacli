package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsParticipantsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "participants",
		Short: "Manage group participants",
	}
	cmd.AddCommand(newGroupsParticipantsListCmd(flags))
	cmd.AddCommand(newGroupsParticipantsLabelsCmd(flags))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "add"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "remove"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "promote"))
	cmd.AddCommand(newGroupsParticipantsActionCmd(flags, "demote"))
	return cmd
}

func newGroupsParticipantsListCmd(flags *rootFlags) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List participants (local snapshot; sync --refresh-groups to update)",
		Long: `List participants from the last group snapshot saved in the local database.

This command does not connect to WhatsApp. The snapshot can be empty or stale.
Run "wacli sync --once --refresh-groups" to fetch joined groups and replace
their local participant snapshots.

LABEL is the member label shown under a member's name in this group, as last
seen in a history sync or one of the member's messages. Group info from
WhatsApp does not carry labels, so a member who has not been seen yet has none.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			participants, err := a.DB().ListGroupParticipants(group)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, participants)
			}

			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "USER JID\tROLE\tLABEL\tUPDATED")
			for _, p := range participants {
				updated := "-"
				if !p.UpdatedAt.IsZero() {
					updated = p.UpdatedAt.Local().Format("2006-01-02 15:04:05")
				}
				label := p.Label
				if label == "" {
					label = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.UserJID, p.Role, label, updated)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsParticipantsLabelsCmd(flags *rootFlags) *cobra.Command {
	var group, user string
	cmd := &cobra.Command{
		Use:   "labels",
		Short: "Show the history of member labels (local database)",
		Long: `Show every member label seen in a group, oldest first per member.

Each row is one label a member had from SET AT on. An empty label means the
member cleared it. SOURCE says where it was seen: history_sync (the group
roster in a history sync), label_change (the member changed it while wacli
was syncing) or message (one of the member's messages; SET AT is then the
earliest message seen with that label, not the exact change).

This command does not connect to WhatsApp and works in read-only mode.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" {
				return fmt.Errorf("--jid is required")
			}
			userJID := ""
			if strings.TrimSpace(user) != "" {
				j, err := wa.ParseUserOrJID(user)
				if err != nil {
					return err
				}
				userJID = j.String()
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			labels, err := a.DB().ListGroupMemberLabels(group, userJID)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, labels)
			}

			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "USER JID\tLABEL\tSET AT\tSOURCE")
			for _, l := range labels {
				label := l.Label
				if label == "" {
					label = "(cleared)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.UserJID, label, l.SetAt.Local().Format("2006-01-02 15:04:05"), l.Source)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringVar(&user, "user", "", "only this member: phone number or JID")
	return cmd
}

func newGroupsParticipantsActionCmd(flags *rootFlags, action string) *cobra.Command {
	var group string
	var users []string
	cmd := &cobra.Command{
		Use:   action,
		Short: action + " participants",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" || len(users) == 0 {
				return fmt.Errorf("--jid and at least one --user are required")
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

			gjid, err := types.ParseJID(group)
			if err != nil {
				return err
			}
			var jids []types.JID
			for _, u := range users {
				j, err := wa.ParseUserOrJID(u)
				if err != nil {
					return err
				}
				jids = append(jids, j)
			}

			updated, err := a.WA().UpdateGroupParticipants(ctx, gjid, jids, wa.GroupParticipantAction(action))
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, updated)
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringSliceVar(&users, "user", nil, "user phone number (+E164 and formatting ok) or JID (repeatable)")
	return cmd
}
