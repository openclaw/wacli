package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// maxContactVCardBytes bounds a raw --vcard input. Contact cards are small;
// the cap keeps an accidental large file or stdin stream out of the message.
const maxContactVCardBytes = 64 * 1024

type contactMessageSender interface {
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
}

type contactOptions struct {
	DisplayName string
	VCard       string
}

type contactCardInput struct {
	Name   string
	Phones []string
	Org    string
}

func newSendContactCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var name string
	var phones []string
	var org string
	var vcardPath string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "contact",
		Short: "Send a contact card (vCard)",
		Long: "Send a native WhatsApp contact card.\n\n" +
			"Build the card from --name and one or more --phone numbers (with optional --org),\n" +
			"or pass a ready-made vCard with --vcard FILE (use - for stdin).",
		Example: "  wacli send contact --to 15551234567 --name \"Jane Doe\" --phone \"+1 555 0100\"\n" +
			"  wacli send contact --to \"Family\" --vcard ./jane.vcf",
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return fmt.Errorf("--to is required")
			}
			var raw string
			if vcardPath != "" {
				if len(phones) > 0 || org != "" {
					return fmt.Errorf("--vcard cannot be combined with --phone or --org")
				}
				data, err := readContactVCard(vcardPath, cmd.InOrStdin())
				if err != nil {
					return err
				}
				raw = data
			}
			opts, err := resolveContactOptions(contactCardInput{Name: name, Phones: phones, Org: org}, raw, vcardPath != "")
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
					Kind:           "contact",
					To:             to,
					Pick:           pick,
					Name:           opts.DisplayName,
					VCard:          opts.VCard,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "contact", resp)
				}
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(ctx); err != nil {
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
				return sendContactMessage(ctx, a.WA(), toJID, opts)
			})
			if err != nil {
				return err
			}

			storeErr := persistOutboundContact(ctx, a, toJID, string(msgID), opts, time.Now().UTC())
			warnSendStoreFailure(os.Stderr, string(msgID), storeErr)

			waitForPostSendRetryReceipts(ctx, postSendWait)

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, addStoreWarning(map[string]any{
					"sent": true,
					"to":   toJID.String(),
					"id":   msgID,
				}, storeErr))
			}
			fmt.Fprintf(os.Stdout, "Sent contact to %s (id %s)\n", toJID.String(), msgID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "recipient JID, phone number, or contact/group/chat name")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&name, "name", "", "contact's full name shown on the card (required unless --vcard has an FN line)")
	cmd.Flags().StringArrayVar(&phones, "phone", nil, "contact's phone number in international format, e.g. +15550100 (repeatable)")
	cmd.Flags().StringVar(&org, "org", "", "optional organization shown on the card")
	cmd.Flags().StringVar(&vcardPath, "vcard", "", "send this vCard file as-is instead of building one (- reads stdin)")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func readContactVCard(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(stdin, maxContactVCardBytes+1))
		if err == nil && len(data) > maxContactVCardBytes {
			err = fmt.Errorf("vCard too large; maximum size is %d bytes", maxContactVCardBytes)
		}
	} else {
		data, err = readRegularFileLimited(path, maxContactVCardBytes)
	}
	if err != nil {
		return "", fmt.Errorf("read --vcard: %w", err)
	}
	return string(data), nil
}

// resolveContactOptions validates the flags and returns the card to send:
// a raw vCard when useRaw is set, otherwise one built from input.
func resolveContactOptions(input contactCardInput, raw string, useRaw bool) (contactOptions, error) {
	name := strings.TrimSpace(input.Name)
	if useRaw {
		return validateRawContactVCard(name, raw)
	}
	if name == "" {
		return contactOptions{}, fmt.Errorf("--name is required")
	}
	if len(input.Phones) == 0 {
		return contactOptions{}, fmt.Errorf("at least one --phone is required")
	}
	digits := make([]string, 0, len(input.Phones))
	for _, phone := range input.Phones {
		normalized, err := normalizeContactPhone(phone)
		if err != nil {
			return contactOptions{}, err
		}
		digits = append(digits, normalized)
	}
	return contactOptions{
		DisplayName: name,
		VCard:       buildContactVCard(name, digits, strings.TrimSpace(input.Org)),
	}, nil
}

func validateRawContactVCard(name, raw string) (contactOptions, error) {
	vcard := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if vcard == "" {
		return contactOptions{}, fmt.Errorf("--vcard is empty")
	}
	upper := strings.ToUpper(vcard)
	if !strings.HasPrefix(upper, "BEGIN:VCARD") || !strings.HasSuffix(upper, "END:VCARD") {
		return contactOptions{}, fmt.Errorf("--vcard must contain a single card from BEGIN:VCARD to END:VCARD")
	}
	if strings.Count(upper, "BEGIN:VCARD") != 1 {
		return contactOptions{}, fmt.Errorf("--vcard must contain exactly one card")
	}
	if name == "" {
		name, _ = wa.ContactDetails(&waProto.ContactMessage{Vcard: proto.String(vcard)})
	}
	if name == "" {
		return contactOptions{}, fmt.Errorf("--name is required when the vCard has no FN line")
	}
	return contactOptions{DisplayName: name, VCard: vcard}, nil
}

// normalizeContactPhone returns the digits of an international phone number,
// accepting the same formatting as --to (spaces, dashes, dots, parentheses
// and one leading +).
func normalizeContactPhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return "", fmt.Errorf("--phone must not be empty")
	}
	if strings.Contains(phone, "@") {
		return "", fmt.Errorf("invalid --phone %q: pass a phone number, not a JID", phone)
	}
	jid, err := wa.ParseUserOrJID(phone)
	if err != nil {
		return "", fmt.Errorf("invalid --phone: %w", err)
	}
	return jid.User, nil
}

// buildContactVCard builds a vCard 3.0 card. Each TEL line carries waid= so
// WhatsApp shows the Message and Add contact buttons for the number.
func buildContactVCard(name string, phoneDigits []string, org string) string {
	escapedName := escapeVCardText(name)
	lines := []string{
		"BEGIN:VCARD",
		"VERSION:3.0",
		"N:;" + escapedName + ";;;",
		"FN:" + escapedName,
	}
	if org != "" {
		lines = append(lines, "ORG:"+escapeVCardText(org))
	}
	for _, digits := range phoneDigits {
		lines = append(lines, fmt.Sprintf("TEL;type=CELL;waid=%s:+%s", digits, digits))
	}
	lines = append(lines, "END:VCARD")
	return strings.Join(lines, "\n")
}

func escapeVCardText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	replacer := strings.NewReplacer(`\`, `\\`, `,`, `\,`, `;`, `\;`, "\n", `\n`, "\r", `\n`)
	return replacer.Replace(value)
}

func buildContactMessage(opts contactOptions) *waProto.Message {
	return &waProto.Message{ContactMessage: &waProto.ContactMessage{
		DisplayName: proto.String(opts.DisplayName),
		Vcard:       proto.String(opts.VCard),
	}}
}

func sendContactMessage(ctx context.Context, sender contactMessageSender, to types.JID, opts contactOptions) (types.MessageID, error) {
	return sender.SendProtoMessage(ctx, to, buildContactMessage(opts))
}

func persistOutboundContact(ctx context.Context, a *app.App, chat types.JID, msgID string, opts contactOptions, now time.Time) error {
	return persistOutboundContactWith(ctx, a.DB(), a.WA(), chat, msgID, opts, now)
}

// persistOutboundContactWith stores the card with the same text sync gives a
// received contact card, so it shows up in history and search right away.
func persistOutboundContactWith(ctx context.Context, db *store.DB, resolver outboundTextResolver, chat types.JID, msgID string, opts contactOptions, now time.Time) error {
	chat = canonicalOutboundChat(ctx, resolver, chat)
	chatName := resolver.ResolveChatName(ctx, chat, "")
	text := wa.ContactDisplayText(buildContactMessage(opts).GetContactMessage())
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
		Text:        text,
		DisplayText: text,
	}); err != nil {
		storeErr = errors.Join(storeErr, fmt.Errorf("message update: %w", err))
	}
	return storeErr
}

func executeDelegatedContact(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	opts, err := validateRawContactVCard(strings.TrimSpace(req.Name), req.VCard)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	if len(opts.VCard) > maxContactVCardBytes {
		return sendDelegateResponse{}, fmt.Errorf("vCard too large; maximum size is %d bytes", maxContactVCardBytes)
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
		return sendContactMessage(ctx, a.WA(), toJID, opts)
	})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	storeErr := persistOutboundContact(ctx, a, toJID, string(msgID), opts, time.Now().UTC())
	waitForPostSendRetryReceipts(ctx, millisDuration(req.PostSendWaitMS, 0))
	resp := sendDelegateResponse{OK: true, Sent: true, To: toJID.String(), ID: string(msgID)}
	if storeErr != nil {
		resp.StoreWarning = storeErr.Error()
	}
	return resp, nil
}
