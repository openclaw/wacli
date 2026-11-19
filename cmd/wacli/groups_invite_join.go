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

func newGroupsInviteCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Manage group invite links",
	}
	cmd.AddCommand(newGroupsInviteLinkCmd(flags))
	return cmd
}

func newGroupsInviteLinkCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Get or revoke invite links",
	}
	cmd.AddCommand(newGroupsInviteLinkGetCmd(flags))
	cmd.AddCommand(newGroupsInviteLinkRevokeCmd(flags))
	return cmd
}

func newGroupsInviteLinkGetCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get invite link",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			return runGroupsInviteLink(flags, jidStr, false)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsInviteLinkRevokeCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke/reset invite link",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}
			return runGroupsInviteLink(flags, jidStr, true)
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func runGroupsInviteLink(flags *rootFlags, jidStr string, reset bool) error {
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		if flags.isReadOnly() {
			// `get` connects to WhatsApp, which read-only mode refuses; never
			// hand it to a writable sync process instead.
			return fmt.Errorf("read-only mode: command would connect to WhatsApp")
		}
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
			Kind:             groupInviteLinkKind,
			To:               jidStr,
			GroupResetInvite: reset,
		})
		if delegated {
			if delegateErr != nil {
				return explainGroupDelegateError(delegateErr, groupInviteLinkKind, "groups invite link")
			}
			return writeGroupInviteLinkResult(flags, resp.To, resp.Link, reset)
		}
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	gjid, err := types.ParseJID(jidStr)
	if err != nil {
		return err
	}
	link, err := a.WA().GetGroupInviteLink(ctx, gjid, reset)
	if err != nil {
		return err
	}
	return writeGroupInviteLinkResult(flags, gjid.String(), link, reset)
}

func newGroupsJoinCmd(flags *rootFlags) *cobra.Command {
	var code string
	cmd := &cobra.Command{
		Use:   "join",
		Short: "Join group by invite code",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(code) == "" {
				return fmt.Errorf("--code is required")
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
			jid, err := a.WA().JoinGroupWithLink(ctx, code)
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, jid); err == nil && info != nil {
				_ = persistGroupInfo(ctx, a.DB(), a.WA(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid.String(), "joined": true})
			}
			fmt.Fprintf(os.Stdout, "Joined: %s\n", jid.String())
			return nil
		},
	}
	cmd.Flags().StringVar(&code, "code", "", "invite code (from link)")
	return cmd
}
