package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

func newListsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use: "lists", Aliases: []string{"labels"},
		Short: "Read and manage WhatsApp Lists and Business labels",
	}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List observed definitions; membership completeness is unknown",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			result, err := a.DB().Lists()
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, result)
			}
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "ID\tNAME\tTYPE\tCOLOR")
			for _, l := range result.Lists {
				kind, color := "unknown", "unknown"
				if l.Type != nil {
					kind = fmt.Sprint(*l.Type)
				}
				if l.Color != nil {
					color = fmt.Sprint(*l.Color)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", sanitize(l.ID), sanitize(l.Name), kind, color)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Membership completeness: unknown; full-sync label events seen: %t.\n", result.FullSyncEventsSeen)
			return nil
		},
	})
	cmd.AddCommand(newListsCreateCmd(flags), newListsEditCmd(flags), newListsDeleteCmd(flags))
	return cmd
}
