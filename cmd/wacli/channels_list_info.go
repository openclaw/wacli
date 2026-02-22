package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

func newChannelsListCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List subscribed channels (live)",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, list)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tJID\tROLE\tDESCRIPTION")
			for _, meta := range list {
				if meta == nil {
					continue
				}
				name := wa.NewsletterName(meta)
				if name == "" {
					name = meta.ID.String()
				}
				desc := ""
				if meta.ThreadMeta.Description.Text != "" {
					desc = truncate(strings.ReplaceAll(meta.ThreadMeta.Description.Text, "\n", " "), 50)
				}
				role := ""
				if meta.ViewerMeta != nil {
					role = string(meta.ViewerMeta.Role)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", truncate(name, 40), meta.ID.String(), role, desc)
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
		Short: "Fetch channel info (live)",
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

			meta, err := a.WA().GetNewsletterInfo(ctx, jid)
			if err != nil {
				return err
			}
			if meta == nil {
				return fmt.Errorf("channel not found")
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, meta)
			}

			name := wa.NewsletterName(meta)
			if name == "" {
				name = meta.ID.String()
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nName: %s\nDescription: %s\nState: %s\n",
				meta.ID.String(),
				name,
				meta.ThreadMeta.Description.Text,
				meta.State.Type,
			)
			if meta.ViewerMeta != nil {
				fmt.Fprintf(os.Stdout, "Role: %s\nMute: %s\n", meta.ViewerMeta.Role, meta.ViewerMeta.Mute)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "channel JID (…@newsletter)")
	return cmd
}
