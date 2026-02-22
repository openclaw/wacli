package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newChannelsJoinCmd(flags *rootFlags) *cobra.Command {
	var invite string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join a channel via invite link or code",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(invite) == "" {
				return fmt.Errorf("--invite is required (full link or code after https://whatsapp.com/channel/)")
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

			meta, err := a.WA().GetNewsletterInfoWithInvite(ctx, invite)
			if err != nil {
				return err
			}
			if meta == nil {
				return fmt.Errorf("could not resolve channel from invite")
			}

			if err := a.WA().FollowNewsletter(ctx, meta.ID); err != nil {
				return err
			}

			name := wa.NewsletterName(meta)
			if name == "" {
				name = meta.ID.String()
			}
			now := time.Now().UTC()
			_ = a.DB().UpsertChat(meta.ID.String(), "newsletter", name, now)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"jid":   meta.ID.String(),
					"name":  name,
					"joined": true,
				})
			}
			fmt.Fprintf(os.Stdout, "Joined channel %s (%s).\n", name, meta.ID.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&invite, "invite", "", "invite link or code (e.g. https://whatsapp.com/channel/... or just the code)")
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

			jid, err := types.ParseJID(jidStr)
			if err != nil {
				return err
			}
			if jid.Server != types.NewsletterServer {
				return fmt.Errorf("JID must be a channel (…@newsletter)")
			}

			if err := a.WA().UnfollowNewsletter(ctx, jid); err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"left": jid.String()})
			}
			fmt.Fprintf(os.Stdout, "Left channel %s.\n", jid.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "channel JID (…@newsletter)")
	return cmd
}
