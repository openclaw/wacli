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
)

const (
	appStateWriteKind      = "app_state_write"
	appStateResyncKind     = "app_state_resync"
	quickReplyCreateAction = "quick_reply_create"
	quickReplyEditAction   = "quick_reply_edit"
	quickReplyDeleteAction = "quick_reply_delete"
)

type appStateWriteRequest struct {
	Action   string    `json:"action"`
	Ref      string    `json:"ref,omitempty"`
	Shortcut *string   `json:"shortcut,omitempty"`
	Message  *string   `json:"message,omitempty"`
	Keywords *[]string `json:"keywords,omitempty"`
}

type appStateWriteResult struct {
	Action     string            `json:"action"`
	QuickReply *store.QuickReply `json:"quick_reply,omitempty"`
}

type appStateWriteApp interface {
	CreateQuickReply(context.Context, app.QuickReplyInput) (store.QuickReply, error)
	EditQuickReply(context.Context, string, app.QuickReplyInput) (store.QuickReply, error)
	DeleteQuickReply(context.Context, string) (store.QuickReply, error)
}

func (r appStateWriteRequest) validate() error {
	switch r.Action {
	case quickReplyCreateAction:
		if r.Shortcut == nil || r.Message == nil {
			return fmt.Errorf("quick reply shortcut and message are required")
		}
	case quickReplyEditAction, quickReplyDeleteAction:
		if strings.TrimSpace(r.Ref) == "" {
			return fmt.Errorf("a quick reply ID or shortcut is required")
		}
	default:
		return fmt.Errorf("unsupported app state write action %q", r.Action)
	}
	return nil
}

func executeAppStateWrite(ctx context.Context, a appStateWriteApp, req appStateWriteRequest) (appStateWriteResult, error) {
	if err := req.validate(); err != nil {
		return appStateWriteResult{}, err
	}
	in := app.QuickReplyInput{Shortcut: req.Shortcut, Message: req.Message, Keywords: req.Keywords}
	var q store.QuickReply
	var err error
	switch req.Action {
	case quickReplyCreateAction:
		q, err = a.CreateQuickReply(ctx, in)
	case quickReplyEditAction:
		q, err = a.EditQuickReply(ctx, req.Ref, in)
	default:
		q, err = a.DeleteQuickReply(ctx, req.Ref)
	}
	if err != nil {
		return appStateWriteResult{}, err
	}
	return appStateWriteResult{Action: req.Action, QuickReply: &q}, nil
}

func executeDelegatedAppStateWrite(ctx context.Context, a appStateWriteApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.AppState == nil {
		return sendDelegateResponse{}, fmt.Errorf("app state write request is missing its payload")
	}
	res, err := executeAppStateWrite(ctx, a, *req.AppState)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Action: res.Action, AppState: &res}, nil
}

func explainAppStateDelegateError(err error, kind, feature string) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), kind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support %s and changed nothing; restart `wacli sync` after upgrading, then run this again: %w", feature, err)
}

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
			return explainAppStateDelegateError(delegateErr, appStateWriteKind, "quick reply writes")
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
	remove, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() { a.WA().Disconnect(); remove() }()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	result, err := executeAppStateWrite(ctx, a, req)
	if err != nil {
		return err
	}
	return writeAppStateWriteResult(flags, result)
}

func writeAppStateWriteResult(flags *rootFlags, res appStateWriteResult) error {
	if res.QuickReply == nil {
		return fmt.Errorf("the running sync process did not report the quick reply result; check before retrying")
	}
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"ok": true, "action": res.Action, "quick_reply": res.QuickReply})
	}
	fmt.Fprintf(os.Stdout, "%s: quick reply %s (%s)\n", res.Action, res.QuickReply.ID, sanitize(res.QuickReply.Shortcut))
	return nil
}

func newQuickRepliesCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "quick-replies", Aliases: []string{"quick-reply"}, Short: "Read and manage WhatsApp Business quick replies"}
	cmd.AddCommand(&cobra.Command{Use: "list", Short: "List observed quick replies and full-sync completeness", Args: cobra.NoArgs,
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
				fmt.Fprintln(os.Stderr, "Completeness: incomplete; no reply-aware full regular sync observed. Run `wacli app-state resync regular` to fetch existing replies.")
			}
			return nil
		},
	})
	var shortcut, message string
	var keywords []string
	create := &cobra.Command{Use: "create", Short: "Create a quick reply", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if shortcut == "" || message == "" {
			return fmt.Errorf("--shortcut and --message are required")
		}
		req := appStateWriteRequest{Action: quickReplyCreateAction, Shortcut: &shortcut, Message: &message}
		if cmd.Flags().Changed("keyword") {
			req.Keywords = &keywords
		}
		return runAppStateWrite(flags, req)
	}}
	create.Flags().StringVar(&shortcut, "shortcut", "", "shortcut typed after / in WhatsApp")
	create.Flags().StringVar(&message, "message", "", "reply text")
	create.Flags().StringArrayVar(&keywords, "keyword", nil, "keyword (repeatable)")
	cmd.AddCommand(create)
	var editShortcut, editMessage string
	var editKeywords []string
	var clearKeywords bool
	edit := &cobra.Command{Use: "edit ID_OR_SHORTCUT", Short: "Change a quick reply", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
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
	}}
	edit.Flags().StringVar(&editShortcut, "shortcut", "", "new shortcut")
	edit.Flags().StringVar(&editMessage, "message", "", "new reply text")
	edit.Flags().StringArrayVar(&editKeywords, "keyword", nil, "replace keywords (repeatable)")
	edit.Flags().BoolVar(&clearKeywords, "clear-keywords", false, "remove every keyword")
	cmd.AddCommand(edit, &cobra.Command{Use: "delete ID_OR_SHORTCUT", Short: "Delete a quick reply", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return runAppStateWrite(flags, appStateWriteRequest{Action: quickReplyDeleteAction, Ref: args[0]})
	}})
	return cmd
}

type appStateResyncApp interface {
	ResyncAppState(context.Context, string) (app.AppStateResyncResult, error)
}

func executeDelegatedAppStateResync(ctx context.Context, a appStateResyncApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.Collection != "regular" {
		return sendDelegateResponse{}, fmt.Errorf("app-state resync supports only regular")
	}
	res, err := a.ResyncAppState(ctx, req.Collection)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, AppStateResync: &res}, nil
}

func newAppStateCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "app-state", Short: "Manage WhatsApp app-state synchronization"}
	cmd.AddCommand(&cobra.Command{Use: "resync regular", Short: "Request a full regular sync using existing recovery", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "regular" {
			return fmt.Errorf("app-state resync supports only regular")
		}
		return runAppStateResync(flags, args[0])
	}})
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
			return explainAppStateDelegateError(delegateErr, appStateResyncKind, "app-state resync")
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
	res, err := a.ResyncAppState(ctx, collection)
	if err != nil {
		return err
	}
	return writeAppStateResyncResult(flags, res)
}

func writeAppStateResyncResult(flags *rootFlags, res app.AppStateResyncResult) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, res)
	}
	fmt.Fprintf(os.Stdout, "%s resync: %s (%d quick replies)\n", res.Collection, res.Completeness, res.QuickReplies)
	return nil
}
