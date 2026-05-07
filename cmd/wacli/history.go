package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

func newHistoryCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "History coverage and backfill",
	}
	cmd.AddCommand(newHistoryCoverageCmd(flags))
	cmd.AddCommand(newHistoryFillCmd(flags))
	cmd.AddCommand(newHistoryBackfillCmd(flags))
	return cmd
}

func newHistoryCoverageCmd(flags *rootFlags) *cobra.Command {
	var chats []string
	var query string
	var kind string
	var limit int
	var includeBlocked bool
	var onlyActionable bool

	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Show local archive coverage by chat",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			coverage, err := a.DB().ListHistoryCoverage(store.ListHistoryCoverageParams{
				ChatJIDs:       chats,
				Query:          query,
				Kind:           kind,
				Limit:          limit,
				IncludeBlocked: includeBlocked,
				OnlyActionable: onlyActionable,
			})
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"coverage": coverage})
			}
			return writeHistoryCoverageTable(os.Stdout, coverage, fullTableOutput(flags.fullOutput), false)
		},
	}
	cmd.Flags().StringSliceVar(&chats, "chat", nil, "chat JID to inspect (repeatable)")
	cmd.Flags().StringVar(&query, "query", "", "filter chats by local name or JID")
	cmd.Flags().StringVar(&kind, "kind", "", "chat kind filter (dm|group|broadcast|newsletter|unknown)")
	cmd.Flags().IntVar(&limit, "limit", 100, "limit rows")
	cmd.Flags().BoolVar(&includeBlocked, "include-blocked", false, "include chats without a local message anchor")
	cmd.Flags().BoolVar(&onlyActionable, "only-actionable", false, "show only chats with a local message anchor")
	return cmd
}

func newHistoryFillCmd(flags *rootFlags) *cobra.Command {
	var chats []string
	var query string
	var kind string
	var limit int
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "fill",
		Short: "Plan multi-chat history backfill",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun {
				return fmt.Errorf("history fill currently supports --dry-run only; use history backfill --chat JID to request history")
			}

			ctx, cancel := withTimeout(cmd.Context(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			coverage, err := a.DB().ListHistoryCoverage(store.ListHistoryCoverageParams{
				ChatJIDs:       chats,
				Query:          query,
				Kind:           kind,
				Limit:          limit,
				IncludeBlocked: true,
			})
			if err != nil {
				return err
			}
			selected := historyFillCandidates(coverage)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"selected": selected,
					"coverage": coverage,
				})
			}

			fmt.Fprintf(os.Stdout, "Selected %d chats for fill dry run.\n", len(selected))
			return writeHistoryCoverageTable(os.Stdout, coverage, fullTableOutput(flags.fullOutput), true)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show which chats would be selected without connecting")
	cmd.Flags().StringSliceVar(&chats, "chat", nil, "chat JID to consider (repeatable)")
	cmd.Flags().StringVar(&query, "query", "", "filter chats by local name or JID")
	cmd.Flags().StringVar(&kind, "kind", "", "chat kind filter (dm|group|broadcast|newsletter|unknown)")
	cmd.Flags().IntVar(&limit, "limit", 100, "limit rows")
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
		Short: "Request older messages for a chat from your primary device (on-demand history sync)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" {
				return fmt.Errorf("--chat is required")
			}
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, stop := signalContextWithEvents(out.NewEventWriter(os.Stderr, flags.events))
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
	cmd.Flags().IntVar(&count, "count", app.DefaultBackfillCount, "number of messages to request per on-demand sync")
	cmd.Flags().IntVar(&requests, "requests", app.DefaultBackfillRequests, "number of on-demand requests to attempt")
	cmd.Flags().DurationVar(&wait, "wait", 60*time.Second, "time to wait for an on-demand response per request")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 5*time.Second, "exit after being idle (after backfill requests)")
	return cmd
}

func historyFillCandidates(coverage []store.HistoryCoverage) []store.HistoryCoverage {
	out := make([]store.HistoryCoverage, 0, len(coverage))
	for _, c := range coverage {
		if c.Status == store.HistoryCoverageStatusReady {
			out = append(out, c)
		}
	}
	return out
}

func writeHistoryCoverageTable(dst io.Writer, coverage []store.HistoryCoverage, fullOutput, includeSelected bool) error {
	w := newTableWriter(dst)
	if includeSelected {
		fmt.Fprintln(w, "SELECTED\tCHAT\tKIND\tMESSAGES\tOLDEST\tNEWEST\tSTATUS\tDETAIL")
	} else {
		fmt.Fprintln(w, "CHAT\tKIND\tMESSAGES\tOLDEST\tNEWEST\tSTATUS\tDETAIL")
	}
	for _, c := range coverage {
		name := c.Name
		if strings.TrimSpace(name) == "" {
			name = c.ChatJID
		}
		detail := historyCoverageDetail(c)
		selected := ""
		if includeSelected {
			if c.Status == store.HistoryCoverageStatusReady {
				selected = "yes"
			} else {
				selected = "no"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
				selected,
				tableCell(name, 32, fullOutput),
				c.Kind,
				c.MessageCount,
				formatHistoryDate(c.OldestTS),
				formatHistoryDate(c.NewestTS),
				c.Status,
				tableCell(detail, 36, fullOutput),
			)
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			tableCell(name, 32, fullOutput),
			c.Kind,
			c.MessageCount,
			formatHistoryDate(c.OldestTS),
			formatHistoryDate(c.NewestTS),
			c.Status,
			tableCell(detail, 36, fullOutput),
		)
	}
	_ = w.Flush()
	return nil
}

func historyCoverageDetail(c store.HistoryCoverage) string {
	if c.BlockedReason != "" {
		return c.BlockedReason
	}
	return c.ChatJID
}

func formatHistoryDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}
