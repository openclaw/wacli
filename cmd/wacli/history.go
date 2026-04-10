package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
)

func newHistoryCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Archive coverage and history backfill",
	}
	cmd.AddCommand(newHistoryCoverageCmd(flags))
	cmd.AddCommand(newHistoryFillCmd(flags))
	cmd.AddCommand(newHistoryBackfillCmd(flags))
	return cmd
}

func newHistoryCoverageCmd(flags *rootFlags) *cobra.Command {
	var query string
	var kind string
	var limit int
	var includeBlocked bool
	var onlyActionable bool

	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Show local archive coverage and backfill state",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			coverage, err := a.ListBackfillCoverage(app.BackfillFillOptions{
				Query:          query,
				Kind:           kind,
				LimitChats:     limit,
				IncludeBlocked: includeBlocked || !onlyActionable,
				OnlyActionable: onlyActionable,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"coverage": coverage})
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CHAT\tKIND\tLAST\tMESSAGES\tOLDEST\tNEWEST\tSTATUS\tDETAIL")
			for _, c := range coverage {
				name := c.Name
				if name == "" {
					name = c.ChatJID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
					truncate(name, 28),
					c.Kind,
					formatMaybeTime(c.LastMessageTS),
					c.MessageCount,
					formatMaybeTime(c.OldestTS),
					formatMaybeTime(c.NewestTS),
					c.Status,
					truncate(historyCoverageDetail(c), 36),
				)
			}
			_ = w.Flush()
			return nil
		},
	}

	cmd.Flags().StringVar(&query, "query", "", "filter chats by local name or JID")
	cmd.Flags().StringVar(&kind, "kind", "", "chat kind filter (dm|group|broadcast|unknown)")
	cmd.Flags().IntVar(&limit, "limit", 100, "limit rows")
	cmd.Flags().BoolVar(&includeBlocked, "include-blocked", false, "include blocked chats in the output")
	cmd.Flags().BoolVar(&onlyActionable, "only-actionable", false, "show only chats that can be worked on now")
	return cmd
}

func newHistoryFillCmd(flags *rootFlags) *cobra.Command {
	var chats []string
	var query string
	var kind string
	var all bool
	var resume bool
	var dryRun bool
	var retryStalled bool
	var limitChats int
	var requestsPerChat int
	var count int
	var wait time.Duration
	var idleExit time.Duration

	cmd := &cobra.Command{
		Use:   "fill",
		Short: "Backfill older history across many chats",
		RunE: func(cmd *cobra.Command, args []string) error {
			fillOpts := app.BackfillFillOptions{
				ChatJIDs:        chats,
				Query:           query,
				Kind:            kind,
				LimitChats:      limitChats,
				RequestsPerChat: requestsPerChat,
				Count:           count,
				WaitPerRequest:  wait,
				IdleExit:        idleExit,
				RetryStalled:    retryStalled,
				ResumeOnly:      resume,
				ResetInProgress: true,
			}

			if dryRun {
				ctx, cancel := withTimeout(context.Background(), flags)
				defer cancel()

				a, lk, err := newApp(ctx, flags, false, false)
				if err != nil {
					return err
				}
				defer closeApp(a, lk)

				plan, err := a.PlanFillHistory(fillOpts)
				if err != nil {
					return err
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, plan)
				}

				fmt.Fprintf(os.Stdout, "Selected %d chats for fill.\n", plan.Selected)
				printHistoryFillChats(plan.Chats)
				return nil
			}

			_ = all
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.FillHistory(ctx, fillOpts)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}

			fmt.Fprintf(os.Stdout, "Fill complete. Attempted %d chats, added %d messages.\n", res.Attempted, res.MessagesAdded)
			printHistoryFillChats(res.Chats)
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&chats, "chat", nil, "chat JID to fill (repeatable)")
	cmd.Flags().StringVar(&query, "query", "", "filter chats by local name or JID")
	cmd.Flags().StringVar(&kind, "kind", "", "chat kind filter (dm|group|broadcast|unknown)")
	cmd.Flags().BoolVar(&all, "all", false, "consider all matching chats (default when no selectors are set)")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume a previous fill run using persisted state")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show which chats would be selected without connecting")
	cmd.Flags().BoolVar(&retryStalled, "retry-stalled", false, "include chats currently marked stalled")
	cmd.Flags().IntVar(&limitChats, "limit-chats", 100, "maximum chats to consider")
	cmd.Flags().IntVar(&requestsPerChat, "requests-per-chat", 3, "max on-demand requests per chat")
	cmd.Flags().IntVar(&count, "count", 50, "messages to request per on-demand sync")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for each on-demand response")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "exit after being idle once all requests finish")
	return cmd
}

func newHistoryBackfillCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var count int
	var requests int
	var wait time.Duration
	var idleExit time.Duration

	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Request older messages for one chat from your primary device",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" {
				return fmt.Errorf("--chat is required")
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			res, err := a.BackfillHistory(ctx, app.BackfillOptions{
				ChatJID:        chat,
				Count:          count,
				Requests:       requests,
				WaitPerRequest: wait,
				IdleExit:       idleExit,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"chat":            res.ChatJID,
					"requests_sent":   res.RequestsSent,
					"responses_seen":  res.ResponsesSeen,
					"messages_added":  res.MessagesAdded,
					"messages_synced": res.MessagesSynced,
				})
			}

			fmt.Fprintf(os.Stdout, "Backfill complete for %s. Added %d messages (%d requests).\n", res.ChatJID, res.MessagesAdded, res.RequestsSent)
			return nil
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID")
	cmd.Flags().IntVar(&count, "count", 50, "number of messages to request per on-demand sync (recommended: 50)")
	cmd.Flags().IntVar(&requests, "requests", 1, "number of on-demand requests to attempt")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for an on-demand response per request")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "exit after being idle (after backfill requests)")
	return cmd
}

func printHistoryFillChats(chats []app.BackfillFillChatResult) {
	if len(chats) == 0 {
		fmt.Fprintln(os.Stdout, "No chats selected.")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CHAT\tSTATUS\tREQUESTS\tADDED\tDETAIL")
	for _, chat := range chats {
		name := chat.Name
		if name == "" {
			name = chat.ChatJID
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\n",
			truncate(name, 28),
			chat.Status,
			chat.RequestsSent,
			chat.MessagesAdded,
			truncate(historyFillDetail(chat), 40),
		)
	}
	_ = w.Flush()
}

func historyCoverageDetail(c store.ChatCoverage) string {
	if c.BlockedReason != "" {
		return c.BlockedReason
	}
	if c.LastError != "" {
		return c.LastError
	}
	if !c.LastBackfillAt.IsZero() {
		return "last backfill " + c.LastBackfillAt.Local().Format("2006-01-02")
	}
	return ""
}

func historyFillDetail(chat app.BackfillFillChatResult) string {
	parts := make([]string, 0, 3)
	if chat.BlockedReason != "" {
		parts = append(parts, chat.BlockedReason)
	}
	if chat.LastError != "" {
		parts = append(parts, chat.LastError)
	}
	if chat.ReachedStart {
		parts = append(parts, "reached start")
	}
	return strings.Join(parts, "; ")
}

func formatMaybeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}
