package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/rpc"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types/events"
)

type daemonOptions struct {
	transport   string
	listen      string
	eventBuffer int
	allowQR     bool
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

	return cmd
}

func runDaemon(ctx context.Context, flags *rootFlags, opts daemonOptions) error {
	a, lk, err := newApp(ctx, flags, true, opts.allowQR)
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

	// Bridge whatsmeow events → EventHub.
	handlerID := a.WA().AddEventHandler(func(rawEvt interface{}) {
		switch v := rawEvt.(type) {
		case *events.Message:
			pm := wa.ParseLiveMessage(v)
			payload := map[string]any{
				"id":        pm.ID,
				"chatJid":   pm.Chat.String(),
				"senderJid": pm.SenderJID,
				"fromMe":    pm.FromMe,
				"text":      pm.Text,
				"timestamp": pm.Timestamp.UTC().Format(time.RFC3339Nano),
			}
			if pm.PushName != "" {
				payload["pushName"] = pm.PushName
			}
			if pm.ReactionEmoji != "" {
				payload["reactionEmoji"] = pm.ReactionEmoji
				payload["reactionToId"] = pm.ReactionToID
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
