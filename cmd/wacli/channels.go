package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newChannelsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channels",
		Short: "Manage WhatsApp channels",
	}
	cmd.AddCommand(newChannelsListCmd(flags))
	cmd.AddCommand(newChannelsInfoCmd(flags))
	cmd.AddCommand(newChannelsJoinCmd(flags))
	cmd.AddCommand(newChannelsLeaveCmd(flags))
	return cmd
}

func newChannelsListCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subscribed channels (live) and update local chats",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			list, err := a.WA().GetSubscribedNewsletters(ctx)
			if err != nil {
				return err
			}
			rows := channelRecords(list)
			persistChannelRecords(a.DB(), rows)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, rows)
			}

			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "NAME\tJID\tROLE\tSTATE\tSUBSCRIBERS\tDESCRIPTION")
			fullOutput := fullTableOutput(flags.fullOutput)
			for _, row := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n",
					tableCell(row.Name, 40, fullOutput),
					row.JID,
					row.Role,
					row.State,
					row.Subscribers,
					tableCell(strings.ReplaceAll(row.Description, "\n", " "), 50, fullOutput),
				)
			}
			_ = w.Flush()
			return nil
		},
	}
	return cmd
}

func newChannelsInfoCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Fetch channel info (live) and update local chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			jid, err := parseChannelJID(jidStr)
			if err != nil {
				return err
			}
			meta, err := a.WA().GetNewsletterInfo(ctx, jid)
			if err != nil {
				return err
			}
			if meta == nil {
				return fmt.Errorf("channel not found")
			}
			row := channelRecordFromMeta(meta)
			persistChannelRecords(a.DB(), []channelRecord{row})

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, row)
			}

			fmt.Fprintf(os.Stdout, "JID: %s\nName: %s\nDescription: %s\nState: %s\nSubscribers: %d\n",
				row.JID,
				row.Name,
				row.Description,
				row.State,
				row.Subscribers,
			)
			if row.Role != "" {
				fmt.Fprintf(os.Stdout, "Role: %s\nMute: %s\n", row.Role, row.Mute)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "channel JID (...@newsletter)")
	return cmd
}

func newChannelsJoinCmd(flags *rootFlags) *cobra.Command {
	var invite string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a channel via invite link or code",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(invite) == "" {
				return fmt.Errorf("--invite is required")
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			meta, err := a.WA().GetNewsletterInfoWithInvite(ctx, strings.TrimSpace(invite))
			if err != nil {
				return err
			}
			if meta == nil {
				return fmt.Errorf("could not resolve channel from invite")
			}
			if err := a.WA().FollowNewsletter(ctx, meta.ID); err != nil {
				return err
			}
			row := channelRecordFromMeta(meta)
			persistChannelRecords(a.DB(), []channelRecord{row})

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"joined": true, "channel": row})
			}
			fmt.Fprintf(os.Stdout, "Joined channel %s (%s).\n", row.Name, row.JID)
			return nil
		},
	}
	cmd.Flags().StringVar(&invite, "invite", "", "invite link or code, e.g. https://whatsapp.com/channel/...")
	return cmd
}

func newChannelsLeaveCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "leave",
		Short: "Leave (unfollow) a channel",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			jid, err := parseChannelJID(jidStr)
			if err != nil {
				return err
			}
			if err := a.WA().UnfollowNewsletter(ctx, jid); err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"left": true, "jid": jid.String()})
			}
			fmt.Fprintf(os.Stdout, "Left channel %s.\n", jid.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "channel JID (...@newsletter)")
	return cmd
}

type channelRecord struct {
	JID         string `json:"jid"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Role        string `json:"role,omitempty"`
	Mute        string `json:"mute,omitempty"`
	State       string `json:"state,omitempty"`
	Subscribers int    `json:"subscribers,omitempty"`
}

func channelRecords(list []*types.NewsletterMetadata) []channelRecord {
	rows := make([]channelRecord, 0, len(list))
	for _, meta := range list {
		if meta == nil {
			continue
		}
		rows = append(rows, channelRecordFromMeta(meta))
	}
	return rows
}

func channelRecordFromMeta(meta *types.NewsletterMetadata) channelRecord {
	row := channelRecord{
		JID:         meta.ID.String(),
		Name:        wa.NewsletterName(meta),
		Description: strings.TrimSpace(meta.ThreadMeta.Description.Text),
		State:       string(meta.State.Type),
		Subscribers: meta.ThreadMeta.SubscriberCount,
	}
	if row.Name == "" {
		row.Name = row.JID
	}
	if meta.ViewerMeta != nil {
		row.Role = string(meta.ViewerMeta.Role)
		row.Mute = string(meta.ViewerMeta.Mute)
	}
	return row
}

func persistChannelRecords(db *store.DB, rows []channelRecord) {
	now := time.Now().UTC()
	for _, row := range rows {
		_ = db.UpsertChat(row.JID, "newsletter", row.Name, now)
	}
}

func parseChannelJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.NewsletterServer {
		return types.JID{}, fmt.Errorf("JID must be a channel (...@newsletter)")
	}
	return jid, nil
}
