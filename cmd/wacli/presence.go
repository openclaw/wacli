package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newPresenceCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "presence",
		Short: "Send typing indicators, watch a contact's online state",
	}
	cmd.AddCommand(newPresenceTypingCmd(flags))
	cmd.AddCommand(newPresencePausedCmd(flags))
	cmd.AddCommand(newPresenceSubscribeCmd(flags))
	return cmd
}

const presenceSubscribeKind = "presence_subscribe"

func newPresenceSubscribeCmd(flags *rootFlags) *cobra.Command {
	var to string
	var wait time.Duration

	cmd := &cobra.Command{
		Use:   "subscribe",
		Short: "Ask WhatsApp for a contact's presence (online, last seen)",
		Long: "Ask WhatsApp to send a contact's presence to this device and print the first\n" +
			"answer. WhatsApp applies the contact's privacy settings: nothing comes back\n" +
			"when they hide their online status or last seen from this account.\n\n" +
			"While `sync --follow` runs, the subscription is made on its connection: the\n" +
			"sync renews it after every reconnect and, with --events, reports each change\n" +
			"as a `presence` event, and the contact's typing as `chat_presence`. Otherwise\n" +
			"wacli connects, shows as online while it waits (WhatsApp sends presence only\n" +
			"to devices that are online), prints the answer and disconnects.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" {
				return fmt.Errorf("--to is required")
			}
			if wait < 0 {
				return fmt.Errorf("--wait must be >= 0")
			}
			return runPresenceSubscribe(flags, to, wait)
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "contact phone number (+E164 and formatting ok) or JID")
	cmd.Flags().DurationVar(&wait, "wait", 10*time.Second, "how long to wait for the contact's presence (0 = subscribe and return at once)")
	return cmd
}

func runPresenceSubscribe(flags *rootFlags, to string, wait time.Duration) error {
	if err := flags.requireWritable(); err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
			Kind:           presenceSubscribeKind,
			To:             to,
			PresenceWaitMS: wait.Milliseconds(),
		})
		if delegated {
			if delegateErr != nil {
				return delegateErr
			}
			return writePresenceSubscribeOutput(flags, resp.To, wait, resp.Presence)
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
	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return err
	}
	if err := a.WA().SendPresence(ctx, types.PresenceAvailable); err != nil {
		return fmt.Errorf("show as online: %w", err)
	}
	// Do not stay online after the answer: the connection closes next.
	defer func() {
		offCtx, offCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer offCancel()
		_ = a.WA().SendPresence(offCtx, types.PresenceUnavailable)
	}()
	state, err := a.WatchPresence(ctx, toJID, wait)
	if err != nil {
		return err
	}
	return writePresenceSubscribeOutput(flags, toJID.String(), wait, state)
}

func writePresenceSubscribeOutput(flags *rootFlags, to string, wait time.Duration, state *app.PresenceState) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"subscribed": true,
			"to":         to,
			"presence":   state,
		})
	}
	switch {
	case state != nil && state.Online:
		fmt.Fprintf(os.Stdout, "%s is online\n", state.JID)
	case state != nil && state.LastSeen != nil:
		fmt.Fprintf(os.Stdout, "%s was last seen %s\n", state.JID, state.LastSeen.Local().Format("2006-01-02 15:04:05 -0700"))
	case state != nil:
		fmt.Fprintf(os.Stdout, "%s is offline (last seen not shared)\n", state.JID)
	case wait > 0:
		fmt.Fprintf(os.Stdout, "No presence from %s within %s (hidden from this account, or not sent)\n", to, wait)
	default:
		fmt.Fprintf(os.Stdout, "Watching the presence of %s\n", to)
	}
	return nil
}

func newPresenceTypingCmd(flags *rootFlags) *cobra.Command {
	var to string
	var media string

	cmd := &cobra.Command{
		Use:   "typing",
		Short: "Send a 'composing' (typing) indicator to a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPresence(flags, to, types.ChatPresenceComposing, media)
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number (+E164 and formatting ok) or JID")
	cmd.Flags().StringVar(&media, "media", "", "media type: 'audio' for recording indicator (default: typing text)")
	return cmd
}

func newPresencePausedCmd(flags *rootFlags) *cobra.Command {
	var to string

	cmd := &cobra.Command{
		Use:   "paused",
		Short: "Send a 'paused' indicator (stop typing) to a chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPresence(flags, to, types.ChatPresencePaused, "")
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient phone number (+E164 and formatting ok) or JID")
	return cmd
}

func runPresence(flags *rootFlags, to string, state types.ChatPresence, media string) error {
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("--to is required")
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}

	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
			Kind:          "presence",
			To:            to,
			PresenceState: string(state),
			PresenceMedia: media,
		})
		if delegated {
			if delegateErr != nil {
				return delegateErr
			}
			return writePresenceOutput(flags, state, resp)
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

	toJID, err := wa.ParseUserOrJID(to)
	if err != nil {
		return err
	}

	chatMedia, err := presenceMediaFromString(media)
	if err != nil {
		return err
	}

	if err := sendPresenceWithRetry(ctx, reconnectForSend(a), func(ctx context.Context) error {
		return a.WA().SendChatPresence(ctx, toJID, state, chatMedia)
	}); err != nil {
		return err
	}

	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"sent":  true,
			"to":    toJID.String(),
			"state": string(state),
		})
	}
	fmt.Fprintf(os.Stdout, "Presence '%s' sent to %s\n", state, toJID.String())
	return nil
}

func writePresenceOutput(flags *rootFlags, state types.ChatPresence, resp sendDelegateResponse) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{
			"sent":  true,
			"to":    resp.To,
			"state": string(state),
		})
	}
	fmt.Fprintf(os.Stdout, "Presence '%s' sent to %s\n", state, resp.To)
	return nil
}

func presenceMediaFromString(media string) (types.ChatPresenceMedia, error) {
	switch strings.ToLower(strings.TrimSpace(media)) {
	case "":
		return "", nil
	case "audio":
		return types.ChatPresenceMediaAudio, nil
	default:
		return "", fmt.Errorf("unsupported --media %q (supported: audio)", media)
	}
}

func presenceStateFromString(state string) (types.ChatPresence, error) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case string(types.ChatPresenceComposing):
		return types.ChatPresenceComposing, nil
	case string(types.ChatPresencePaused):
		return types.ChatPresencePaused, nil
	default:
		return "", fmt.Errorf("unsupported presence state %q", state)
	}
}

func sendPresenceWithRetry(ctx context.Context, reconnect func(context.Context) error, send func(context.Context) error) error {
	_, err := runSendOperation(ctx, reconnect, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, send(ctx)
	})
	return err
}
