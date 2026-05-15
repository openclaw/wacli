package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

func newCallsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calls",
		Short: "List WhatsApp call events from the local DB",
	}
	cmd.AddCommand(newCallsListCmd(flags))
	return cmd
}

func newCallsListCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var limit int
	var afterStr string
	var beforeStr string
	var asc bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List call events",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			chatJIDs, err := messageChatJIDFilter(ctx, a, chat)
			if err != nil {
				return err
			}
			calls, err := a.DB().ListCallEvents(store.ListCallEventsParams{
				ChatJIDs: chatJIDs,
				Limit:    limit,
				After:    after,
				Before:   before,
				Asc:      asc,
			})
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"calls": calls})
			}
			return writeCallsList(os.Stdout, calls, fullTableOutput(flags.fullOutput))
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "filter by chat JID")
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of call events to return")
	cmd.Flags().StringVar(&afterStr, "after", "", "only call events after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only call events before time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&asc, "asc", false, "show oldest call events first (default: newest first)")
	return cmd
}

func writeCallsList(dst io.Writer, calls []store.CallEvent, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "TIME\tCHAT\tDIR\tMEDIA\tEVENT\tOUTCOME\tCALL ID")
	for _, c := range calls {
		chatLabel := c.ChatName
		if chatLabel == "" {
			chatLabel = c.ChatJID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			c.Timestamp.Local().Format("2006-01-02 15:04:05"),
			tableCell(chatLabel, 24, fullOutput),
			tableCell(emptyUnknown(c.Direction), 8, fullOutput),
			tableCell(emptyUnknown(c.Media), 8, fullOutput),
			tableCell(c.EventType, 14, fullOutput),
			tableCell(callOutcomeLabel(c), 18, fullOutput),
			tableCell(c.CallID, 18, fullOutput),
		)
	}
	return w.Flush()
}

func callOutcomeLabel(c store.CallEvent) string {
	if c.Outcome != "" {
		if c.DurationSecs > 0 {
			return c.Outcome + " " + formatCallListDuration(c.DurationSecs)
		}
		return c.Outcome
	}
	if c.Reason != "" {
		return c.Reason
	}
	return ""
}

func formatCallListDuration(seconds int64) string {
	if seconds <= 0 {
		return ""
	}
	minutes := seconds / 60
	secs := seconds % 60
	if minutes == 0 {
		return fmt.Sprintf("(%ds)", secs)
	}
	if secs == 0 {
		return fmt.Sprintf("(%dm)", minutes)
	}
	return fmt.Sprintf("(%dm%02ds)", minutes, secs)
}

func emptyUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
