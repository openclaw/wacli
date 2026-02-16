package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/out"
)

func newGroupsRefreshCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Fetch joined groups (live) and update local DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}
			if err := a.Connect(ctx, false, nil); err != nil {
				return err
			}

			gs, err := a.WA().GetJoinedGroups(ctx)
			if err != nil {
				return err
			}
			for _, g := range gs {
				if g == nil {
					continue
				}
				_ = persistGroupInfo(a.DB(), g)
				_ = a.DB().UpsertChat(g.JID.String(), "group", g.GroupName.Name, time.Now())
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"groups": len(gs)})
			}
			fmt.Fprintf(os.Stdout, "Imported %d groups.\n", len(gs))
			return nil
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

			gs, err := a.DB().ListGroups(query, limit)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, gs)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tJID\tCREATED")
			for _, g := range gs {
				name := g.Name
				if name == "" {
					name = g.JID
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", truncate(name, 40), g.JID, g.CreatedAt.Local().Format("2006-01-02"))
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "search query")
	cmd.Flags().IntVar(&limit, "limit", 50, "limit")
	return cmd
}
