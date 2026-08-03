package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

const (
	latitudeBound  = 90
	longitudeBound = 180
)

const locationDisplayText = "Sent location"

const locationMediaType = "location"

type locationMessageSender interface {
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
}

type locationOptions struct {
	Latitude  float64
	Longitude float64
	Name      string
}

func newSendLocationCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var latitude float64
	var longitude float64
	var name string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "location",
		Short: "Send a location pin",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			opts, err := validateLocationOptions(
				latitude,
				longitude,
				name,
				cmd.Flags().Changed("latitude"),
				cmd.Flags().Changed("longitude"),
			)
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
				resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
					Kind:           "location",
					To:             to,
					Pick:           pick,
					Latitude:       opts.Latitude,
					Longitude:      opts.Longitude,
					Name:           opts.Name,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "location", resp)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			toJID, err := resolveRecipient(a, to, recipientOptions{pick: pick, asJSON: flags.asJSON})
			if err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}
			toJID = warmupRecipient(ctx, a.WA(), toJID, os.Stderr)
			if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
				return err
			}

			msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
				return sendLocationMessage(ctx, a.WA(), toJID, opts)
			})
			if err != nil {
				return err
			}

			storeErr := persistOutboundLocation(ctx, a, toJID, string(msgID), opts, time.Now().UTC())
			warnSendStoreFailure(os.Stderr, string(msgID), storeErr)

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, addStoreWarning(map[string]any{
					"sent": true,
					"to":   toJID.String(),
					"id":   msgID,
				}, storeErr))
			}
			fmt.Fprintf(os.Stdout, "Sent location to %s (id %s)\n", toJID.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().Float64Var(&latitude, "latitude", 0, "latitude in decimal degrees (-90 to 90)")
	cmd.Flags().Float64Var(&longitude, "longitude", 0, "longitude in decimal degrees (-180 to 180)")
	cmd.Flags().StringVar(&name, "name", "", "optional place name shown on the pin")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func validateLocationOptions(lat, lng float64, name string, latSet, lngSet bool) (locationOptions, error) {
	if !latSet || !lngSet {
		return locationOptions{}, fmt.Errorf("--latitude and --longitude are required")
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lng) || math.IsInf(lng, 0) {
		return locationOptions{}, fmt.Errorf("--latitude and --longitude must be finite numbers")
	}
	if lat < -latitudeBound || lat > latitudeBound {
		return locationOptions{}, fmt.Errorf("--latitude must be between -%d and %d (got %v)", latitudeBound, latitudeBound, lat)
	}
	if lng < -longitudeBound || lng > longitudeBound {
		return locationOptions{}, fmt.Errorf("--longitude must be between -%d and %d (got %v)", longitudeBound, longitudeBound, lng)
	}
	return locationOptions{Latitude: lat, Longitude: lng, Name: strings.TrimSpace(name)}, nil
}

func buildLocationMessage(opts locationOptions) *waProto.Message {
	loc := &waProto.LocationMessage{
		DegreesLatitude:  proto.Float64(opts.Latitude),
		DegreesLongitude: proto.Float64(opts.Longitude),
	}
	if name := strings.TrimSpace(opts.Name); name != "" {
		loc.Name = proto.String(name)
	}
	return &waProto.Message{LocationMessage: loc}
}

func sendLocationMessage(ctx context.Context, sender locationMessageSender, to types.JID, opts locationOptions) (types.MessageID, error) {
	return sender.SendProtoMessage(ctx, to, buildLocationMessage(opts))
}

func persistOutboundLocation(ctx context.Context, a *app.App, chat types.JID, msgID string, opts locationOptions, now time.Time) error {
	return persistOutboundLocationWith(ctx, a.DB(), a.WA(), chat, msgID, opts, now)
}

func persistOutboundLocationWith(ctx context.Context, db *store.DB, resolver outboundTextResolver, chat types.JID, msgID string, opts locationOptions, now time.Time) error {
	chat = canonicalOutboundChat(ctx, resolver, chat)
	chatName := resolver.ResolveChatName(ctx, chat, "")
	var storeErr error
	if err := db.UpsertChat(chat.String(), chatKindFromJID(chat), chatName, now); err != nil {
		storeErr = fmt.Errorf("chat update: %w", err)
	}
	if err := db.UpsertMessage(store.UpsertMessageParams{
		ChatJID:     chat.String(),
		ChatName:    chatName,
		MsgID:       msgID,
		SenderJID:   "",
		SenderName:  "me",
		Timestamp:   now,
		FromMe:      true,
		DisplayText: locationDisplayText,
		MediaType:   locationMediaType,
	}); err != nil {
		storeErr = errors.Join(storeErr, fmt.Errorf("message update: %w", err))
	}
	if err := db.UpsertMessageLocation(store.MessageLocation{
		ChatJID:   chat.String(),
		MsgID:     msgID,
		Latitude:  opts.Latitude,
		Longitude: opts.Longitude,
		Name:      opts.Name,
	}); err != nil {
		storeErr = errors.Join(storeErr, fmt.Errorf("location update: %w", err))
	}
	return storeErr
}

func executeDelegatedLocation(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	opts, err := validateLocationOptions(req.Latitude, req.Longitude, req.Name, true, true)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return sendDelegateResponse{}, err
	}
	msgID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return sendLocationMessage(ctx, a.WA(), toJID, opts)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	storeErr := persistOutboundLocation(ctx, a, toJID, string(msgID), opts, time.Now().UTC())
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: string(msgID)}
	if storeErr != nil {
		resp.StoreWarning = storeErr.Error()
	}
	return resp, nil
}
