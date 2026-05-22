package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
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

const (
	selectResponseList        = "list_response"
	selectResponseButtons     = "buttons_response"
	selectResponseTemplate    = "template_button_reply"
	selectResponseInteractive = "interactive_response"
)

type selectSender interface {
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
}

type selectRequest struct {
	Label        string
	ButtonID     string
	Index        int
	IndexSet     bool
	Type         string
	Sender       string
	PostSendWait time.Duration
}

type selectOption struct {
	Type         string `json:"type"`
	DisplayText  string `json:"display_text"`
	ID           string `json:"id,omitempty"`
	ResponseType string `json:"-"`
	Index        int    `json:"-"`
	Description  string `json:"-"`
}

type selectResult struct {
	Sent     bool         `json:"sent"`
	To       string       `json:"to"`
	ID       string       `json:"id"`
	Target   string       `json:"target"`
	Selected selectOption `json:"selected"`
}

func newSendSelectCmd(flags *rootFlags) *cobra.Command {
	var to string
	var pick int
	var msgID string
	var label string
	var buttonID string
	var index int
	var typ string
	var senderOverride string
	postSendWait := postSendRetryReceiptWait

	cmd := &cobra.Command{
		Use:   "select",
		Short: "Select a stored WhatsApp button or list row",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(to) == "" || strings.TrimSpace(msgID) == "" {
				return fmt.Errorf("--to and --id are required")
			}
			req := selectRequest{
				Label:        label,
				ButtonID:     buttonID,
				Index:        index,
				IndexSet:     cmd.Flags().Changed("index"),
				Type:         typ,
				Sender:       senderOverride,
				PostSendWait: postSendWait,
			}
			if err := validateSelectRequest(req); err != nil {
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
					Kind:           "button_list_select",
					To:             to,
					Pick:           pick,
					ID:             msgID,
					Label:          label,
					ButtonID:       buttonID,
					SelectIndex:    index,
					Type:           typ,
					Sender:         senderOverride,
					PostSendWaitMS: durationMillis(postSendWait),
				})
				if delegated {
					if delegateErr != nil {
						return delegateErr
					}
					return writeDelegatedSendOutput(flags, "button_list_select", resp)
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

			res, err := sendButtonListSelection(ctx, a, toJID, msgID, req)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			fmt.Fprintf(os.Stdout, "Selected %q on %s in %s (id %s)\n", res.Selected.DisplayText, res.Target, res.To, res.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&to, "to", "", "chat JID, phone number, or contact/group/chat name where the message lives")
	cmd.Flags().IntVar(&pick, "pick", 0, "when --to is ambiguous, pick the Nth match (1-indexed)")
	cmd.Flags().StringVar(&msgID, "id", "", "button or list message ID")
	cmd.Flags().StringVar(&label, "label", "", "select the option whose display text exactly matches this value after trimming")
	cmd.Flags().StringVar(&buttonID, "button-id", "", "select the option whose stored button ID exactly matches this value after trimming")
	cmd.Flags().IntVar(&index, "index", 0, "select the Nth selectable option (1-indexed)")
	cmd.Flags().StringVar(&typ, "type", "", "filter selectable options by type: list_row or quick_reply")
	cmd.Flags().StringVar(&senderOverride, "sender", "", "JID of the original message sender when the local store has no sender")
	cmd.Flags().DurationVar(&postSendWait, "post-send-wait", postSendRetryReceiptWait, "keep the connection alive after send so retry receipts can be handled (0 disables)")
	return cmd
}

func validateSelectRequest(req selectRequest) error {
	choices := 0
	if strings.TrimSpace(req.Label) != "" {
		choices++
	}
	if strings.TrimSpace(req.ButtonID) != "" {
		choices++
	}
	if req.IndexSet {
		choices++
		if req.Index < 1 {
			return fmt.Errorf("--index must be 1 or greater")
		}
	}
	if choices != 1 {
		return fmt.Errorf("exactly one of --label, --button-id, or --index is required")
	}
	switch strings.TrimSpace(req.Type) {
	case "", "list_row", "quick_reply":
		return nil
	default:
		return fmt.Errorf("--type must be list_row or quick_reply")
	}
}

func sendButtonListSelection(ctx context.Context, a *app.App, chat types.JID, targetID string, req selectRequest) (selectResult, error) {
	target, chatJID, err := loadSelectTargetMessage(ctx, a, chat, targetID)
	if err != nil {
		return selectResult{}, err
	}
	selected, err := resolveSelectOption(target.Buttons, req)
	if err != nil {
		return selectResult{}, err
	}
	msg, err := buildSelectResponseMessage(chat, selected, target, req.Sender)
	if err != nil {
		return selectResult{}, err
	}
	if err := warnRapidSendIfNeeded(a.StoreDir(), time.Now().UTC(), os.Stderr); err != nil {
		return selectResult{}, err
	}
	sentID, err := runSendOperation(ctx, reconnectForSend(a), func(ctx context.Context) (types.MessageID, error) {
		return sendSelectProto(ctx, a.WA(), chat, msg)
	})
	if err != nil {
		return selectResult{}, err
	}
	now := time.Now().UTC()
	persistOutboundSelection(ctx, a, chat, chatJID, string(sentID), selected, now)
	waitForPostSendRetryReceipts(ctx, req.PostSendWait)
	return selectResult{
		Sent:     true,
		To:       chat.String(),
		ID:       string(sentID),
		Target:   targetID,
		Selected: selected,
	}, nil
}

func sendSelectProto(ctx context.Context, sender selectSender, to types.JID, msg *waProto.Message) (types.MessageID, error) {
	return sender.SendProtoMessage(ctx, to, msg)
}

func loadSelectTargetMessage(ctx context.Context, a *app.App, chat types.JID, msgID string) (store.Message, string, error) {
	var lastErr error = sql.ErrNoRows
	for _, chatJID := range pollChatJIDCandidates(ctx, a, chat) {
		msg, err := a.DB().GetMessage(chatJID, msgID)
		if err == nil {
			if msg.FromMe {
				return store.Message{}, "", fmt.Errorf("message %s in %s is from this account; select requires a stored inbound button or list message", msgID, chatJID)
			}
			return msg, chatJID, nil
		}
		lastErr = err
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		return store.Message{}, "", fmt.Errorf("lookup message: %w", err)
	}
	if errors.Is(lastErr, sql.ErrNoRows) {
		return store.Message{}, "", fmt.Errorf("message %s not found in local store for chat %s; run `wacli sync` first", msgID, chat.String())
	}
	return store.Message{}, "", fmt.Errorf("lookup message: %w", lastErr)
}

func resolveSelectOption(buttons []store.Button, req selectRequest) (selectOption, error) {
	filterType := strings.TrimSpace(req.Type)
	var candidates []store.Button
	for _, b := range buttons {
		if filterType != "" && strings.TrimSpace(b.Type) != filterType {
			continue
		}
		candidates = append(candidates, b)
	}
	if len(candidates) == 0 {
		if filterType != "" {
			return selectOption{}, fmt.Errorf("message has no %s options", filterType)
		}
		return selectOption{}, fmt.Errorf("message has no stored button or list options")
	}
	candidates = selectableButtons(candidates)
	if len(candidates) == 0 {
		return selectOption{}, fmt.Errorf("message has no selectable button or list options")
	}

	var matches []store.Button
	switch {
	case strings.TrimSpace(req.Label) != "":
		label := strings.TrimSpace(req.Label)
		for _, b := range candidates {
			if strings.TrimSpace(b.DisplayText) == label {
				matches = append(matches, b)
			}
		}
	case strings.TrimSpace(req.ButtonID) != "":
		id := strings.TrimSpace(req.ButtonID)
		for _, b := range candidates {
			if strings.TrimSpace(b.ID) == id {
				matches = append(matches, b)
			}
		}
	case req.IndexSet:
		selectable := selectableButtonsByIndex(candidates)
		if req.Index > len(selectable) {
			return selectOption{}, fmt.Errorf("--index %d is out of range; message has %d selectable option(s)", req.Index, len(selectable))
		}
		matches = append(matches, selectable[req.Index-1])
	}

	if len(matches) == 0 {
		return selectOption{}, fmt.Errorf("no matching option found")
	}
	if len(matches) > 1 {
		return selectOption{}, fmt.Errorf("multiple options match; candidates: %s", describeSelectCandidates(matches))
	}
	return normalizeSelectOption(matches[0])
}

func selectableButtons(buttons []store.Button) []store.Button {
	out := make([]store.Button, 0, len(buttons))
	for _, b := range buttons {
		if isSelectableButtonType(b.Type) {
			out = append(out, b)
		}
	}
	return out
}

func selectableButtonsByIndex(buttons []store.Button) []store.Button {
	out := selectableButtons(buttons)
	for _, b := range out {
		if b.Index < 1 {
			return out
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Index < out[j].Index
	})
	return out
}

func normalizeSelectOption(b store.Button) (selectOption, error) {
	typ := strings.TrimSpace(b.Type)
	if !isSelectableButtonType(typ) {
		return selectOption{}, fmt.Errorf("option %q has unsupported control type %q", strings.TrimSpace(b.DisplayText), typ)
	}
	opt := selectOption{
		Type:         typ,
		DisplayText:  strings.TrimSpace(b.DisplayText),
		ID:           strings.TrimSpace(b.ID),
		ResponseType: strings.TrimSpace(b.ResponseType),
		Index:        b.Index,
		Description:  strings.TrimSpace(b.Description),
	}
	if opt.ResponseType == "" {
		switch opt.Type {
		case "list_row":
			opt.ResponseType = selectResponseList
		case "quick_reply":
			return selectOption{}, fmt.Errorf("quick reply %q has no stored response_type; sync this message again with a newer wacli", opt.DisplayText)
		}
	}
	return opt, nil
}

func isSelectableButtonType(typ string) bool {
	switch strings.TrimSpace(typ) {
	case "list_row", "quick_reply":
		return true
	default:
		return false
	}
}

func describeSelectCandidates(buttons []store.Button) string {
	parts := make([]string, 0, len(buttons))
	for _, b := range buttons {
		label := strings.TrimSpace(b.DisplayText)
		id := strings.TrimSpace(b.ID)
		if id != "" {
			parts = append(parts, fmt.Sprintf("%q (%s, id %q)", label, strings.TrimSpace(b.Type), id))
		} else {
			parts = append(parts, fmt.Sprintf("%q (%s)", label, strings.TrimSpace(b.Type)))
		}
	}
	return strings.Join(parts, "; ")
}

func buildSelectResponseMessage(chat types.JID, opt selectOption, target store.Message, senderOverride string) (*waProto.Message, error) {
	if opt.DisplayText == "" {
		return nil, fmt.Errorf("selected option has no display text")
	}
	if opt.ID == "" {
		return nil, fmt.Errorf("selected option %q has no stored ID; sync this message again with a newer wacli", opt.DisplayText)
	}
	ctx, err := buildSelectContextInfo(chat, target, senderOverride)
	if err != nil {
		return nil, err
	}
	switch opt.ResponseType {
	case selectResponseList:
		listType := waProto.ListResponseMessage_SINGLE_SELECT
		resp := &waProto.ListResponseMessage{
			Title:             proto.String(opt.DisplayText),
			ListType:          &listType,
			SingleSelectReply: &waProto.ListResponseMessage_SingleSelectReply{SelectedRowID: proto.String(opt.ID)},
			ContextInfo:       ctx,
		}
		if opt.Description != "" {
			resp.Description = proto.String(opt.Description)
		}
		return &waProto.Message{
			ListResponseMessage: resp,
		}, nil
	case selectResponseButtons:
		respType := waProto.ButtonsResponseMessage_DISPLAY_TEXT
		return &waProto.Message{
			ButtonsResponseMessage: &waProto.ButtonsResponseMessage{
				SelectedButtonID: proto.String(opt.ID),
				Response: &waProto.ButtonsResponseMessage_SelectedDisplayText{
					SelectedDisplayText: opt.DisplayText,
				},
				ContextInfo: ctx,
				Type:        &respType,
			},
		}, nil
	case selectResponseTemplate:
		if opt.Index < 1 {
			return nil, fmt.Errorf("template quick reply %q has no stored index; sync this message again with a newer wacli", opt.DisplayText)
		}
		selectedIndex := uint32(opt.Index - 1)
		return &waProto.Message{
			TemplateButtonReplyMessage: &waProto.TemplateButtonReplyMessage{
				SelectedID:          proto.String(opt.ID),
				SelectedDisplayText: proto.String(opt.DisplayText),
				SelectedIndex:       proto.Uint32(selectedIndex),
				ContextInfo:         ctx,
			},
		}, nil
	case selectResponseInteractive:
		return nil, fmt.Errorf("native-flow quick replies are not supported yet; sync captured phone-tap fixtures before implementing interactive_response")
	default:
		return nil, fmt.Errorf("unsupported response_type %q; sync this message again with a newer wacli", opt.ResponseType)
	}
}

func buildSelectContextInfo(chat types.JID, target store.Message, senderOverride string) (*waProto.ContextInfo, error) {
	stanzaID := strings.TrimSpace(target.MsgID)
	if stanzaID == "" {
		return nil, fmt.Errorf("target message ID is required")
	}
	info := &waProto.ContextInfo{StanzaID: proto.String(stanzaID)}
	jid, err := resolveSelectSender(chat, target, senderOverride)
	if err != nil {
		return nil, err
	}
	if !jid.IsEmpty() {
		info.Participant = proto.String(jid.String())
	}
	return info, nil
}

func resolveSelectSender(chat types.JID, target store.Message, override string) (types.JID, error) {
	if strings.TrimSpace(override) != "" {
		jid, err := wa.ParseUserOrJID(override)
		if err != nil {
			return types.JID{}, fmt.Errorf("invalid --sender: %w", err)
		}
		return jid, nil
	}
	if strings.TrimSpace(target.SenderJID) != "" {
		jid, err := types.ParseJID(target.SenderJID)
		if err != nil {
			return types.JID{}, fmt.Errorf("stored target sender is invalid: %w", err)
		}
		return jid, nil
	}
	if chat.Server == types.GroupServer {
		return types.JID{}, fmt.Errorf("--sender is required for unsynced group selections")
	}
	return types.JID{}, nil
}

func persistOutboundSelection(ctx context.Context, a *app.App, chat types.JID, chatJID, msgID string, selected selectOption, now time.Time) {
	if strings.TrimSpace(chatJID) == "" {
		chatJID = primaryPollChatJID(ctx, a, chat)
	}
	chatName := a.WA().ResolveChatName(ctx, chat, "")
	if err := a.DB().UpsertChat(chatJID, chatKindFromJID(chat), chatName, now); err != nil {
		warnOutboundPersist("chat", msgID, err)
		return
	}
	if err := a.DB().UpsertMessage(store.UpsertMessageParams{
		ChatJID:    chatJID,
		ChatName:   chatName,
		MsgID:      msgID,
		SenderName: "me",
		Timestamp:  now,
		FromMe:     true,
		Text:       "Selected: " + selected.DisplayText,
	}); err != nil {
		warnOutboundPersist("message", msgID, err)
	}
}

func executeDelegatedButtonListSelect(ctx context.Context, a *app.App, req sendDelegateRequest) (sendDelegateResponse, error) {
	if strings.TrimSpace(req.To) == "" || strings.TrimSpace(req.ID) == "" {
		return sendDelegateResponse{}, fmt.Errorf("send select requires --to and --id")
	}
	selectReq := selectRequest{
		Label:        req.Label,
		ButtonID:     req.ButtonID,
		Index:        req.SelectIndex,
		IndexSet:     req.SelectIndex > 0,
		Type:         req.Type,
		Sender:       req.Sender,
		PostSendWait: millisDuration(req.PostSendWaitMS, 0),
	}
	if err := validateSelectRequest(selectReq); err != nil {
		return sendDelegateResponse{}, err
	}
	toJID, err := resolveRecipient(a, req.To, recipientOptions{pick: req.Pick, asJSON: true})
	if err != nil {
		return sendDelegateResponse{}, err
	}
	toJID = warmupDelegatedRecipient(ctx, a, toJID)
	res, err := sendButtonListSelection(ctx, a, toJID, req.ID, selectReq)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{
		OK:             true,
		Sent:           true,
		To:             res.To,
		ID:             res.ID,
		Target:         res.Target,
		SelectedOption: &res.Selected,
	}, nil
}
