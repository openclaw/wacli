package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// appStateWriteKind carries label and quick reply writes to a running
// `sync --follow`. A new kind makes older owners reject the request instead
// of ignoring fields they do not know.
const appStateWriteKind = "app_state_write"
const appStateResyncKind = "app_state_resync"

const (
	labelCreateAction      = "label_create"
	labelEditAction        = "label_edit"
	labelDeleteAction      = "label_delete"
	chatLabelAddAction     = "chat_label_add"
	chatLabelRemoveAction  = "chat_label_remove"
	quickReplyCreateAction = "quick_reply_create"
	quickReplyEditAction   = "quick_reply_edit"
	quickReplyDeleteAction = "quick_reply_delete"
)

type appStateWriteRequest struct {
	Action   string    `json:"action"`
	Ref      string    `json:"ref,omitempty"`
	ID       string    `json:"id,omitempty"`
	Name     *string   `json:"name,omitempty"`
	Color    *int32    `json:"color,omitempty"`
	Shortcut *string   `json:"shortcut,omitempty"`
	Message  *string   `json:"message,omitempty"`
	Keywords *[]string `json:"keywords,omitempty"`
	Chat     string    `json:"chat,omitempty"`
	Pick     int       `json:"pick,omitempty"`
}

type appStateWriteResult struct {
	Action     string            `json:"action"`
	Label      *store.List       `json:"label,omitempty"`
	QuickReply *store.QuickReply `json:"quick_reply,omitempty"`
	Chat       string            `json:"chat,omitempty"`
}

type appStateWriteApp interface {
	recipientResolverApp
	CreateLabel(context.Context, app.LabelInput) (store.List, error)
	EditLabel(context.Context, string, app.LabelInput) (store.List, error)
	DeleteLabel(context.Context, string) (store.List, error)
	SetChatLabel(context.Context, types.JID, string, bool) (app.ChatLabelResult, error)
	CreateQuickReply(context.Context, app.QuickReplyInput) (store.QuickReply, error)
	EditQuickReply(context.Context, string, app.QuickReplyInput) (store.QuickReply, error)
	DeleteQuickReply(context.Context, string) (store.QuickReply, error)
}

func (r appStateWriteRequest) validate() error {
	needsRef := map[string]bool{labelEditAction: true, labelDeleteAction: true, chatLabelAddAction: true,
		chatLabelRemoveAction: true, quickReplyEditAction: true, quickReplyDeleteAction: true}
	switch r.Action {
	case labelCreateAction, labelEditAction, labelDeleteAction, chatLabelAddAction, chatLabelRemoveAction,
		quickReplyCreateAction, quickReplyEditAction, quickReplyDeleteAction:
	default:
		return fmt.Errorf("unsupported app state write action %q", r.Action)
	}
	if needsRef[r.Action] && strings.TrimSpace(r.Ref) == "" {
		return fmt.Errorf("a label or quick reply ID is required")
	}
	if (r.Action == chatLabelAddAction || r.Action == chatLabelRemoveAction) && strings.TrimSpace(r.Chat) == "" {
		return fmt.Errorf("--chat is required")
	}
	return nil
}

// executeAppStateWrite is shared by the direct and the delegated path.
func executeAppStateWrite(ctx context.Context, a appStateWriteApp, req appStateWriteRequest) (appStateWriteResult, error) {
	if err := req.validate(); err != nil {
		return appStateWriteResult{}, err
	}
	res := appStateWriteResult{Action: req.Action}
	label := func(l store.List, err error) (appStateWriteResult, error) {
		if err != nil {
			return appStateWriteResult{}, err
		}
		res.Label = &l
		return res, nil
	}
	quickReply := func(q store.QuickReply, err error) (appStateWriteResult, error) {
		if err != nil {
			return appStateWriteResult{}, err
		}
		res.QuickReply = &q
		return res, nil
	}
	labelInput := app.LabelInput{ID: req.ID, Name: req.Name, Color: req.Color}
	quickReplyInput := app.QuickReplyInput{Shortcut: req.Shortcut, Message: req.Message, Keywords: req.Keywords}
	switch req.Action {
	case labelCreateAction:
		return label(a.CreateLabel(ctx, labelInput))
	case labelEditAction:
		return label(a.EditLabel(ctx, req.Ref, labelInput))
	case labelDeleteAction:
		return label(a.DeleteLabel(ctx, req.Ref))
	case chatLabelAddAction, chatLabelRemoveAction:
		jid, err := resolveRecipient(a, req.Chat, recipientOptions{pick: req.Pick, asJSON: true})
		if err != nil {
			return appStateWriteResult{}, err
		}
		result, err := a.SetChatLabel(ctx, jid, req.Ref, req.Action == chatLabelAddAction)
		res.Chat = result.Chat
		return label(result.Label, err)
	case quickReplyCreateAction:
		return quickReply(a.CreateQuickReply(ctx, quickReplyInput))
	case quickReplyEditAction:
		return quickReply(a.EditQuickReply(ctx, req.Ref, quickReplyInput))
	default:
		return quickReply(a.DeleteQuickReply(ctx, req.Ref))
	}
}

func executeDelegatedAppStateWrite(ctx context.Context, a appStateWriteApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.AppState == nil {
		return sendDelegateResponse{}, fmt.Errorf("app state write request is missing its payload")
	}
	res, err := executeAppStateWrite(ctx, a, *req.AppState)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Action: res.Action, Chat: res.Chat, AppState: &res}, nil
}

func explainAppStateWriteDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), appStateWriteKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support label or quick reply writes and changed nothing; restart `wacli sync` after upgrading, then run this again: %w", err)
}

// runAppStateWrite runs the write here, or hands it to the `sync --follow`
// process that owns the store, as chat archiving does.
func runAppStateWrite(flags *rootFlags, req appStateWriteRequest) error {
	if err := req.validate(); err != nil {
		return err
	}
	if err := flags.requireWritable(); err != nil {
		return err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: appStateWriteKind, AppState: &req})
		if !delegated {
			return err
		}
		if delegateErr != nil {
			return explainAppStateWriteDelegateError(delegateErr)
		}
		if resp.AppState == nil {
			return fmt.Errorf("the running sync process did not report the %s result; check before retrying", req.Action)
		}
		return writeAppStateWriteResult(flags, *resp.AppState)
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() {
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	res, err := executeAppStateWrite(ctx, a, req)
	if err != nil {
		return err
	}
	return writeAppStateWriteResult(flags, res)
}

func writeAppStateWriteResult(flags *rootFlags, res appStateWriteResult) error {
	if flags.asJSON {
		body := map[string]any{"ok": true, "action": res.Action}
		if res.Label != nil {
			body["label"] = res.Label
		}
		if res.QuickReply != nil {
			body["quick_reply"] = res.QuickReply
		}
		if res.Chat != "" {
			body["chat"] = res.Chat
		}
		return out.WriteJSON(os.Stdout, body)
	}
	switch {
	case res.Chat != "" && res.Label != nil:
		fmt.Fprintf(os.Stdout, "%s: %s label %s (%s)\n", res.Action, res.Chat, res.Label.ID, sanitize(res.Label.Name))
	case res.Label != nil:
		fmt.Fprintf(os.Stdout, "%s: label %s (%s)\n", res.Action, res.Label.ID, sanitize(res.Label.Name))
	case res.QuickReply != nil:
		fmt.Fprintf(os.Stdout, "%s: quick reply %s (%s)\n", res.Action, res.QuickReply.ID, sanitize(res.QuickReply.Shortcut))
	default:
		fmt.Fprintf(os.Stdout, "%s\n", res.Action)
	}
	return nil
}

func newListsCreateCmd(flags *rootFlags) *cobra.Command {
	var id, name string
	var color int32
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a label (WhatsApp Business) or custom list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			req := appStateWriteRequest{Action: labelCreateAction, ID: id, Name: &name}
			if cmd.Flags().Changed("color") {
				req.Color = &color
			}
			return runAppStateWrite(flags, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "label name")
	cmd.Flags().Int32Var(&color, "color", 0, "WhatsApp label color index, 0-19 (default 0)")
	cmd.Flags().StringVar(&id, "id", "", "explicit unused label ID; required until a full sync has shown every label")
	return cmd
}

func newListsEditCmd(flags *rootFlags) *cobra.Command {
	var name string
	var color int32
	cmd := &cobra.Command{
		Use:   "edit ID_OR_NAME",
		Short: "Rename or recolor a label",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := appStateWriteRequest{Action: labelEditAction, Ref: args[0]}
			if cmd.Flags().Changed("name") {
				req.Name = &name
			}
			if cmd.Flags().Changed("color") {
				req.Color = &color
			}
			if req.Name == nil && req.Color == nil {
				return fmt.Errorf("nothing to change; pass --name or --color")
			}
			return runAppStateWrite(flags, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new label name")
	cmd.Flags().Int32Var(&color, "color", 0, "new WhatsApp label color index, 0-19")
	return cmd
}

func newListsDeleteCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "delete ID_OR_NAME",
		Short: "Delete a label definition",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppStateWrite(flags, appStateWriteRequest{Action: labelDeleteAction, Ref: args[0]})
		},
	}
}

func newChatsLabelCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "label",
		Short: "Add a chat to, or remove it from, a label",
	}
	for _, add := range []bool{true, false} {
		use, short, action := "add", "Add a chat to a label", chatLabelAddAction
		if !add {
			use, short, action = "remove", "Remove a chat from a label", chatLabelRemoveAction
		}
		var label string
		opts := chatStateOptions{}
		sub := &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if strings.TrimSpace(label) == "" {
					return fmt.Errorf("--label is required")
				}
				return runAppStateWrite(flags, appStateWriteRequest{Action: action, Ref: label, Chat: opts.chat, Pick: opts.pick})
			},
		}
		addChatStateFlags(sub, &opts)
		sub.Flags().StringVar(&label, "label", "", "label ID or exact name (see `wacli lists list`)")
		cmd.AddCommand(sub)
	}
	return cmd
}

func newQuickRepliesCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "quick-replies",
		Aliases: []string{"quick-reply"},
		Short:   "Read and manage WhatsApp Business quick replies",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List observed quick replies and full-sync completeness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			result, err := a.DB().QuickReplies()
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, result)
			}
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "ID\tSHORTCUT\tMESSAGE")
			for _, q := range result.QuickReplies {
				fmt.Fprintf(w, "%s\t%s\t%s\n", sanitize(q.ID), sanitize(q.Shortcut), truncate(sanitize(q.Message), 60))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if result.Completeness == "incomplete" {
				fmt.Fprintln(os.Stderr, "Completeness: incomplete; no full regular sync observed. Run `wacli app-state resync regular` to fetch existing replies.")
			}
			return nil
		},
	})

	var shortcut, message string
	var keywords []string
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a quick reply",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if shortcut == "" || message == "" {
				return fmt.Errorf("--shortcut and --message are required")
			}
			req := appStateWriteRequest{Action: quickReplyCreateAction, Shortcut: &shortcut, Message: &message}
			if cmd.Flags().Changed("keyword") {
				req.Keywords = &keywords
			}
			return runAppStateWrite(flags, req)
		},
	}
	create.Flags().StringVar(&shortcut, "shortcut", "", "shortcut typed after / in WhatsApp")
	create.Flags().StringVar(&message, "message", "", "reply text")
	create.Flags().StringArrayVar(&keywords, "keyword", nil, "keyword (repeatable)")
	cmd.AddCommand(create)

	var editShortcut, editMessage string
	var editKeywords []string
	var clearKeywords bool
	edit := &cobra.Command{
		Use:   "edit ID_OR_SHORTCUT",
		Short: "Change a quick reply",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := appStateWriteRequest{Action: quickReplyEditAction, Ref: args[0]}
			if cmd.Flags().Changed("shortcut") {
				req.Shortcut = &editShortcut
			}
			if cmd.Flags().Changed("message") {
				req.Message = &editMessage
			}
			if clearKeywords && cmd.Flags().Changed("keyword") {
				return fmt.Errorf("--keyword and --clear-keywords are mutually exclusive")
			}
			if clearKeywords {
				empty := []string{}
				req.Keywords = &empty
			} else if cmd.Flags().Changed("keyword") {
				req.Keywords = &editKeywords
			}
			if req.Shortcut == nil && req.Message == nil && req.Keywords == nil {
				return fmt.Errorf("nothing to change; pass --shortcut, --message, --keyword or --clear-keywords")
			}
			return runAppStateWrite(flags, req)
		},
	}
	edit.Flags().StringVar(&editShortcut, "shortcut", "", "new shortcut")
	edit.Flags().StringVar(&editMessage, "message", "", "new reply text")
	edit.Flags().StringArrayVar(&editKeywords, "keyword", nil, "replace keywords (repeatable)")
	edit.Flags().BoolVar(&clearKeywords, "clear-keywords", false, "remove every keyword")
	cmd.AddCommand(edit)

	cmd.AddCommand(&cobra.Command{
		Use:   "delete ID_OR_SHORTCUT",
		Short: "Delete a quick reply",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAppStateWrite(flags, appStateWriteRequest{Action: quickReplyDeleteAction, Ref: args[0]})
		},
	})
	return cmd
}

type appStateResyncApp interface {
	ResyncAppState(context.Context, string) (app.AppStateResyncResult, error)
}

func executeDelegatedAppStateResync(ctx context.Context, a appStateResyncApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Collection != "regular" {
		return sendDelegateResponse{}, fmt.Errorf("app-state resync supports only regular")
	}
	result, err := a.ResyncAppState(ctx, req.Collection)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, AppStateResync: &result}, nil
}

func newAppStateCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "app-state", Short: "Manage WhatsApp app-state synchronization"}
	resync := &cobra.Command{
		Use: "resync regular", Short: "Force a full regular sync using existing recovery", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "regular" {
				return fmt.Errorf("app-state resync supports only regular")
			}
			return runAppStateResync(flags, args[0])
		},
	}
	cmd.AddCommand(resync)
	return cmd
}

func runAppStateResync(flags *rootFlags, collection string) error {
	if err := flags.requireWritable(); err != nil {
		return err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()
	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{Kind: appStateResyncKind, Collection: collection})
		if !delegated {
			return err
		}
		if delegateErr != nil {
			if strings.Contains(delegateErr.Error(), "unsupported send kind") && strings.Contains(delegateErr.Error(), appStateResyncKind) {
				return fmt.Errorf("the running sync process does not support app-state resync and changed nothing; restart `wacli sync` after upgrading, then run this again: %w", delegateErr)
			}
			return delegateErr
		}
		if resp.AppStateResync == nil {
			return fmt.Errorf("the running sync process did not report the resync result; check before retrying")
		}
		return writeAppStateResyncResult(flags, *resp.AppStateResync)
	}
	defer closeApp(a, lk)
	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() { a.WA().Disconnect(); remove() }()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	result, err := a.ResyncAppState(ctx, collection)
	if err != nil {
		return err
	}
	return writeAppStateResyncResult(flags, result)
}

func writeAppStateResyncResult(flags *rootFlags, result app.AppStateResyncResult) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, result)
	}
	fmt.Fprintf(os.Stdout, "%s resync: %s (%d quick replies, %d lists)\n", result.Collection, result.Completeness, result.QuickReplies, result.Lists)
	return nil
}
