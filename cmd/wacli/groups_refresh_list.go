package main

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newGroupsRefreshCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Fetch joined groups (live) and update local DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGroupCommand(flags, groupOp{Action: groupActionRefresh}, true)
		},
	}
	return cmd
}

func newGroupsListCmd(flags *rootFlags) *cobra.Command {
	var query string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List known groups (from local DB; run sync to populate)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if limit <= 0 {
				limit = 50
			}
			fetchLimit := limit
			if fetchLimit < math.MaxInt {
				fetchLimit++
			}
			gs, err := a.DB().ListGroups(query, fetchLimit)
			if err != nil {
				return err
			}
			if len(gs) > limit {
				gs = gs[:limit]
				message := fmt.Sprintf("showing first %d matching groups; more are available; increase --limit", limit)
				if flags.events {
					_ = out.NewEventWriter(os.Stderr, true).Emit("warning", map[string]any{
						"code": "groups_list_truncated", "message": message, "limit": limit,
					})
				} else {
					_ = out.WriteError(os.Stderr, false, fmt.Errorf("warning: %s", message))
				}
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, gs)
			}

			fullOutput := fullTableOutput(flags.fullOutput)
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "NAME\tJID\tTYPE\tPARENT\tCREATED")
			for _, g := range gs {
				name := g.Name
				if name == "" {
					name = g.JID
				}
				parent := g.LinkedParentJID
				if parent == "" {
					parent = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					tableCell(name, 40, fullOutput),
					g.JID,
					groupKindLabel(g.IsParent, g.LinkedParentJID),
					parent,
					g.CreatedAt.Local().Format("2006-01-02"),
				)
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum groups to return (non-positive values use 50)")
	return cmd
}
