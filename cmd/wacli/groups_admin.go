package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	appcore "github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newGroupsCreateCmd(flags *rootFlags) *cobra.Command {
	var name string
	var users []string
	var announceOnly bool
	var locked bool
	var joinApproval bool
	var parent bool
	var linkedParent string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a group",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			if parent && strings.TrimSpace(linkedParent) != "" {
				return fmt.Errorf("--community and --linked-parent cannot be combined")
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

			participants, err := parseGroupUserJIDs(users)
			if err != nil {
				return err
			}
			var parentJID types.JID
			if strings.TrimSpace(linkedParent) != "" {
				parentJID, err = parseGroupJID(linkedParent)
				if err != nil {
					return fmt.Errorf("parse --linked-parent: %w", err)
				}
			}
			info, err := a.WA().CreateGroup(ctx, wa.CreateGroupRequest{
				Name:                   name,
				Participants:           participants,
				IsAnnounce:             announceOnly,
				IsLocked:               locked,
				IsJoinApprovalRequired: joinApproval,
				IsParent:               parent,
				LinkedParentJID:        parentJID,
			})
			if err != nil {
				return err
			}
			if info != nil {
				_ = persistGroupInfo(a.DB(), info)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, info)
			}
			if info == nil {
				fmt.Fprintln(os.Stdout, "OK")
				return nil
			}
			fmt.Fprintf(os.Stdout, "JID: %s\nName: %s\n", info.JID.String(), sanitize(info.GroupName.Name))
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "group name")
	cmd.Flags().StringSliceVar(&users, "user", nil, "initial participant phone number (+E164 and formatting ok) or JID (repeatable)")
	cmd.Flags().BoolVar(&announceOnly, "announce-only", false, "only admins can send messages")
	cmd.Flags().BoolVar(&locked, "locked", false, "only admins can edit group info")
	cmd.Flags().BoolVar(&joinApproval, "join-approval", false, "require admin approval for new join requests")
	cmd.Flags().BoolVar(&parent, "community", false, "create a community parent group")
	cmd.Flags().StringVar(&linkedParent, "linked-parent", "", "community parent group JID for a new subgroup")
	return cmd
}

func newGroupsTopicCmd(flags *rootFlags, use string) *cobra.Command {
	var jidStr string
	var text string
	cmd := &cobra.Command{
		Use:   use,
		Short: "Set group topic/description",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" || !cmd.Flags().Changed("text") {
				return fmt.Errorf("--jid and --text are required")
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := a.WA().SetGroupTopic(ctx, gjid, text); err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(a.DB(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), "topic": text})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringVar(&text, "text", "", "new topic/description text (empty string clears it)")
	return cmd
}

func newGroupsAnnounceOnlyCmd(flags *rootFlags) *cobra.Command {
	return newGroupsToggleCmd(flags, "announce-only", "Set whether only admins can send messages", func(ctx context.Context, client appcore.WAClient, jid types.JID, enabled bool) error {
		return client.SetGroupAnnounce(ctx, jid, enabled)
	}, "announce_only")
}

func newGroupsLockedCmd(flags *rootFlags) *cobra.Command {
	return newGroupsToggleCmd(flags, "locked", "Set whether only admins can edit group info", func(ctx context.Context, client appcore.WAClient, jid types.JID, enabled bool) error {
		return client.SetGroupLocked(ctx, jid, enabled)
	}, "locked")
}

func newGroupsToggleCmd(flags *rootFlags, use, short string, apply func(context.Context, appcore.WAClient, types.JID, bool) error, jsonKey string) *cobra.Command {
	var jidStr string
	var on bool
	var off bool
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" {
				return fmt.Errorf("--jid is required")
			}
			enabled, err := parseOnOffFlags(cmd, on, off)
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			if err := apply(ctx, a.WA(), gjid, enabled); err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(a.DB(), info)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": gjid.String(), jsonKey: enabled})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().BoolVar(&on, "on", false, "enable setting")
	cmd.Flags().BoolVar(&off, "off", false, "disable setting")
	return cmd
}

func newGroupsRequestsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "Manage group join requests",
	}
	cmd.AddCommand(newGroupsRequestsListCmd(flags))
	cmd.AddCommand(newGroupsRequestsActionCmd(flags, "approve"))
	cmd.AddCommand(newGroupsRequestsActionCmd(flags, "reject"))
	return cmd
}

// requestListEntry is the JSON/text output shape for a single pending join request.
type requestListEntry struct {
	JID         string    `json:"JID"`
	PhoneNumber string    `json:"phone_number,omitempty"`
	RequestedAt time.Time `json:"RequestedAt"`
}

// resolveRequestEntries converts raw join-request participants to output entries,
// resolving LID JIDs to phone numbers via the provided resolver function.
func resolveRequestEntries(ctx context.Context, requests []types.GroupParticipantRequest, resolve func(context.Context, types.JID) types.JID) []requestListEntry {
	entries := make([]requestListEntry, len(requests))
	for i, req := range requests {
		pn := resolve(ctx, req.JID)
		phone := ""
		if pn.Server == types.DefaultUserServer {
			phone = "+" + pn.User
		}
		entries[i] = requestListEntry{
			JID:         req.JID.String(),
			PhoneNumber: phone,
			RequestedAt: req.RequestedAt,
		}
	}
	return entries
}

func newGroupsRequestsListCmd(flags *rootFlags) *cobra.Command {
	var jidStr string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending group join requests",
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
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			requests, err := a.WA().GetGroupRequestParticipants(ctx, gjid)
			if err != nil {
				return err
			}

			entries := resolveRequestEntries(ctx, requests, a.WA().ResolveLIDToPN)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, entries)
			}
			for _, e := range entries {
				fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", e.JID, e.RequestedAt.Local().Format("2006-01-02 15:04:05"), e.PhoneNumber)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	return cmd
}

func newGroupsRequestsActionCmd(flags *rootFlags, action string) *cobra.Command {
	var jidStr string
	var users []string
	cmd := &cobra.Command{
		Use:   action,
		Short: action + " group join requests",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jidStr) == "" || len(users) == 0 {
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

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			gjid, err := parseGroupJID(jidStr)
			if err != nil {
				return err
			}
			jids, err := parseGroupUserJIDs(users)
			if err != nil {
				return err
			}
			updated, err := a.WA().UpdateGroupRequestParticipants(ctx, gjid, jids, wa.GroupParticipantRequestAction(action))
			if err != nil {
				return err
			}
			if info, err := a.WA().GetGroupInfo(ctx, gjid); err == nil && info != nil {
				_ = persistGroupInfo(a.DB(), info)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, updated)
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&jidStr, "jid", "", "group JID (…@g.us)")
	cmd.Flags().StringSliceVar(&users, "user", nil, "requesting user phone number (+E164 and formatting ok) or JID (repeatable)")
	return cmd
}

func parseOnOffFlags(cmd *cobra.Command, on, off bool) (bool, error) {
	onChanged := cmd.Flags().Changed("on")
	offChanged := cmd.Flags().Changed("off")
	if onChanged == offChanged {
		return false, fmt.Errorf("exactly one of --on or --off is required")
	}
	if onChanged {
		if !on {
			return false, fmt.Errorf("--on=false does not select a mode; use --off to disable")
		}
		return true, nil
	}
	if !off {
		return false, fmt.Errorf("--off=false does not select a mode; use --on to enable")
	}
	return false, nil
}

func parseGroupJID(raw string) (types.JID, error) {
	jid, err := types.ParseJID(strings.TrimSpace(raw))
	if err != nil {
		return types.JID{}, err
	}
	if jid.Server != types.GroupServer {
		return types.JID{}, fmt.Errorf("expected group JID ending in @g.us, got %s", jid.String())
	}
	return jid, nil
}

func parseGroupUserJIDs(users []string) ([]types.JID, error) {
	jids := make([]types.JID, 0, len(users))
	for _, user := range users {
		jid, err := wa.ParseUserOrJID(user)
		if err != nil {
			return nil, err
		}
		jids = append(jids, jid)
	}
	return jids, nil
}
