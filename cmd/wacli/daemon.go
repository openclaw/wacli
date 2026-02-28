package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/rpc"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type daemonOptions struct {
	transport   string
	listen      string
	eventBuffer int
	allowQR     bool
	lockWait    time.Duration
}

func newDaemonCmd(flags *rootFlags) *cobra.Command {
	var opts daemonOptions

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start a JSON-RPC 2.0 daemon (stdio or TCP transport)",
		Long: `Run wacli as a persistent JSON-RPC 2.0 daemon.

The daemon acquires an exclusive store lock so that it is the single WhatsApp
session owner.  Clients communicate via JSON-RPC 2.0 requests/responses.
Server-initiated event notifications (e.g. message.received) are pushed to
clients that have called the "subscribe" method.

Supported RPC methods:
  send           – send a text message
  listChats      – list chats from local store
  getMessages    – retrieve message history for a chat
  sendReaction   – send a reaction to a message
  remoteDelete   – revoke/delete a message
  sendFile       – upload and send a file attachment
  searchMessages – full-text search across messages
  subscribe      – subscribe to real-time events (push notifications)

Transport:
  stdio  (default) – JSON-RPC on stdin/stdout; ideal for sub-process integration
  tcp              – listen on --listen address`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDaemon(ctx, flags, opts)
		},
	}

	cmd.Flags().StringVar(&opts.transport, "transport", "stdio", "transport type: stdio or tcp")
	cmd.Flags().StringVar(&opts.listen, "listen", "127.0.0.1:8686", "TCP listen address (only used with --transport=tcp)")
	cmd.Flags().IntVar(&opts.eventBuffer, "event-buffer", 256, "per-subscriber event channel buffer size")
	cmd.Flags().BoolVar(&opts.allowQR, "allow-qr", false, "allow QR code authentication on first run")
	cmd.Flags().DurationVar(&opts.lockWait, "lock-wait", 45*time.Second, "how long to wait for store lock before failing")

	return cmd
}

func runDaemon(ctx context.Context, flags *rootFlags, opts daemonOptions) error {
	a, lk, err := newAppWithLockWait(ctx, flags, true, opts.allowQR, opts.lockWait)
	if err != nil {
		return fmt.Errorf("init app: %w", err)
	}
	defer closeApp(a, lk)

	// Connect to WhatsApp.
	if err := a.Connect(ctx, opts.allowQR, func(qr string) {
		fmt.Fprintf(os.Stderr, "Scan QR code: %s\n", qr)
	}); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}

	// Event hub.
	hub := rpc.NewHub(opts.eventBuffer)
	defer hub.Close()

	reconnectRequested := make(chan string, 1)
	var reconnecting atomic.Bool
	requestReconnect := func(reason string) {
		if ctx.Err() != nil {
			return
		}
		select {
		case reconnectRequested <- reason:
		default:
		}
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-reconnectRequested:
				if !reconnecting.CompareAndSwap(false, true) {
					continue
				}
				fmt.Fprintf(os.Stderr, "wacli: reconnecting to WhatsApp (%s)\n", reason)
				if err := a.WA().ReconnectWithBackoff(ctx, 2*time.Second, 30*time.Second); err != nil {
					if ctx.Err() == nil {
						fmt.Fprintf(os.Stderr, "wacli: reconnect failed: %v\n", err)
					}
				} else {
					fmt.Fprintln(os.Stderr, "wacli: WhatsApp reconnected")
					hub.Publish(rpc.Event{
						Type: "connection",
						Payload: map[string]any{
							"state": "connected",
						},
					})
				}
				reconnecting.Store(false)
			}
		}
	}()

	// Defensive watchdog in case the disconnect event is missed.
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if !a.WA().IsConnected() {
					requestReconnect("connection watchdog")
				}
			}
		}
	}()

	// Resolve own JID for self-identification in events.
	selfJid := a.WA().SelfJID()

	// Pre-populate group names into local DB so event handler can look them up.
	if groups, err := a.WA().GetJoinedGroups(ctx); err == nil {
		for _, g := range groups {
			_ = a.DB().UpsertGroup(g.JID.String(), g.GroupName.Name, g.OwnerJID.String(), g.GroupCreated)
			_ = a.DB().UpsertChat(g.JID.String(), "group", g.GroupName.Name, time.Time{})
		}
		fmt.Fprintf(os.Stderr, "Synced %d group(s) to local DB\n", len(groups))
	}

	// In-memory group name cache for fast lookup in event handler.
	groupNameCache := make(map[string]string)
	if chats, err := a.DB().ListChats("", 200); err == nil {
		for _, c := range chats {
			if strings.Contains(c.JID, "@g.us") && c.Name != "" {
				groupNameCache[c.JID] = c.Name
			}
		}
	}

	// Bridge whatsmeow events → EventHub.
	resolveChatJIDForSend := func(chat types.JID) string {
		base := chat.ToNonAD()
		if !wa.IsLIDJID(base) {
			return base.String()
		}
		resolveCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		resolved, err := a.WA().ResolveRecipientJID(resolveCtx, base)
		if err != nil || resolved.IsEmpty() {
			return base.String()
		}
		return resolved.ToNonAD().String()
	}

	handlerID := a.WA().AddEventHandler(func(rawEvt interface{}) {
		switch v := rawEvt.(type) {
		case *events.Message:
			pm := wa.ParseLiveMessage(v)
			chatJID := pm.Chat.String()
			resolvedJID := chatJID
			if !wa.IsGroupJID(pm.Chat) && !pm.Chat.IsBroadcastList() {
				resolvedJID = resolveChatJIDForSend(pm.Chat)
			}
			payload := map[string]any{
				"id":          pm.ID,
				"chatJid":     chatJID,
				"resolvedJid": resolvedJID,
				"senderJid":   pm.SenderJID,
				"selfJid":     selfJid,
				"fromMe":      pm.FromMe,
				"text":        pm.Text,
				"timestamp":   pm.Timestamp.UTC().Format(time.RFC3339Nano),
			}
			if strings.Contains(chatJID, "@g.us") {
				if name, ok := groupNameCache[chatJID]; ok {
					payload["groupName"] = name
				} else if chat, err := a.DB().GetChat(chatJID); err == nil && strings.TrimSpace(chat.Name) != "" {
					payload["groupName"] = chat.Name
					groupNameCache[chatJID] = chat.Name
				} else {
					// Fallback: fetch from WA server.
					groupJID, parseErr := types.ParseJID(chatJID)
					if parseErr == nil {
						groupInfo, infoErr := a.WA().GetGroupInfo(ctx, groupJID)
						if infoErr == nil && groupInfo != nil && groupInfo.GroupName.Name != "" {
							payload["groupName"] = groupInfo.GroupName.Name
							groupNameCache[chatJID] = groupInfo.GroupName.Name
							// Persist to DB for future lookups.
							_ = a.DB().UpsertChat(chatJID, "group", groupInfo.GroupName.Name, time.Time{})
						}
					}
				}
			}
			if pm.PushName != "" {
				payload["pushName"] = pm.PushName
			}
			if pm.ReactionEmoji != "" {
				payload["reactionEmoji"] = pm.ReactionEmoji
				payload["reactionToId"] = pm.ReactionToID
			}
			if len(pm.MentionedJids) > 0 {
				payload["mentionedJids"] = pm.MentionedJids
			}
			if pm.Media != nil {
				payload["media"] = map[string]any{
					"type":       pm.Media.Type,
					"mimeType":   pm.Media.MimeType,
					"caption":    pm.Media.Caption,
					"directPath": pm.Media.DirectPath,
					"fileLength": pm.Media.FileLength,
				}
			}
			hub.Publish(rpc.Event{
				Type:    "message.received",
				Payload: payload,
			})

		case *events.Receipt:
			hub.Publish(rpc.Event{
				Type: "message.sent",
				Payload: map[string]any{
					"ids":     v.MessageIDs,
					"chatJid": v.MessageSource.Chat.String(),
				},
			})

		case *events.ChatPresence:
			hub.Publish(rpc.Event{
				Type: "typing",
				Payload: map[string]any{
					"chatJid":   v.MessageSource.Chat.String(),
					"senderJid": v.MessageSource.Sender.String(),
					"state":     string(v.State),
					"media":     string(v.Media),
				},
			})

		case *events.Presence:
			status := "available"
			if v.Unavailable {
				status = "unavailable"
			}
			payload := map[string]any{
				"from":   v.From.String(),
				"status": status,
			}
			if !v.LastSeen.IsZero() {
				payload["lastSeen"] = v.LastSeen.UTC().Format(time.RFC3339Nano)
			}
			hub.Publish(rpc.Event{
				Type:    "presence",
				Payload: payload,
			})

		case *events.Connected:
			hub.Publish(rpc.Event{
				Type: "connection",
				Payload: map[string]any{
					"state": "connected",
				},
			})

		case *events.Disconnected:
			hub.Publish(rpc.Event{
				Type: "connection",
				Payload: map[string]any{
					"state": "disconnected",
				},
			})
			requestReconnect("disconnected event")
		}
	})
	defer a.WA().RemoveEventHandler(handlerID)

	// Start RPC server.
	srv := rpc.NewServer(a, hub)
	fmt.Fprintf(os.Stderr, "wacli: daemon started (transport=%s)\n", opts.transport)

	return srv.Serve(ctx, rpc.ServeOptions{
		Transport: opts.transport,
		Listen:    opts.listen,
	})
}
