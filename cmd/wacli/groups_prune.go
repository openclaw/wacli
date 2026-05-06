package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/app"
	"github.com/steipete/wacli/internal/out"
	"github.com/steipete/wacli/internal/store"
)

func newGroupsPruneCmd(flags *rootFlags) *cobra.Command {
	var days int
	var leftOnly bool
	var dryRun bool
	var confirm bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old or left groups from local storage",
		Long: `Clean up groups that you have left or that have been inactive.

By default, removes groups you have left. Use --days to also remove groups
with no messages in the last N days. Use --dry-run to preview what would be deleted.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			_ = ctx

			if leftOnly || days <= 0 {
				return pruneLeftGroups(a, days, dryRun, confirm, flags.asJSON)
			}

			return pruneOldGroups(a, days, dryRun, confirm, flags.asJSON)
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "also remove groups with no messages in the last N days (0 = only remove left groups)")
	cmd.Flags().BoolVar(&leftOnly, "left-only", true, "only remove groups you have left (default behavior)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "skip confirmation prompt")
	return cmd
}

func pruneLeftGroups(a *app.App, days int, dryRun, confirm, asJSON bool) error {
	var groups []store.Group
	var err error

	if days > 0 {
		deleted, err := a.DB().DeleteLeftGroupsOlderThan(days)
		if err != nil {
			return err
		}
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"deleted": deleted})
		}
		fmt.Fprintf(os.Stderr, "Deleted %d left group(s) older than %d days.\n", deleted, days)
		return nil
	}

	groups, err = a.DB().ListLeftGroups()
	if err != nil {
		return err
	}

	if len(groups) == 0 {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"deleted": 0, "message": "no left groups to prune"})
		}
		fmt.Fprintln(os.Stderr, "No left groups to prune.")
		return nil
	}

	if dryRun {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"would_delete": len(groups), "groups": groups})
		}
		fmt.Fprintf(os.Stderr, "Would delete %d left group(s):\n", len(groups))
		for _, g := range groups {
			name := g.Name
			if name == "" {
				name = g.JID
			}
			fmt.Fprintf(os.Stderr, "  - %s (%s)\n", name, g.JID)
		}
		fmt.Fprintln(os.Stderr, "\nRun without --dry-run to actually delete.")
		return nil
	}

	if !confirm {
		fmt.Fprintf(os.Stderr, "About to delete %d left group(s). This cannot be undone.\n", len(groups))
		fmt.Fprint(os.Stderr, "Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	deleted, err := a.DB().DeleteLeftGroups()
	if err != nil {
		return err
	}

	if asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"deleted": deleted})
	}
	fmt.Fprintf(os.Stderr, "Done. Deleted %d left group(s).\n", deleted)
	return nil
}

func pruneOldGroups(a *app.App, days int, dryRun, confirm, asJSON bool) error {
	groups, err := a.DB().ListGroups("", 0)
	if err != nil {
		return err
	}

	var toDelete []store.Group
	for _, g := range groups {
		if !g.LeftAt.IsZero() {
			toDelete = append(toDelete, g)
			continue
		}
		chat, err := a.DB().GetChat(g.JID)
		if err != nil {
			continue
		}
		if days > 0 && !chat.LastMessageTS.IsZero() {
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			if chat.LastMessageTS.Before(cutoff) {
				toDelete = append(toDelete, g)
			}
		}
	}

	if len(toDelete) == 0 {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"deleted": 0, "message": "no groups to prune"})
		}
		fmt.Fprintln(os.Stderr, "No groups to prune.")
		return nil
	}

	if dryRun {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"would_delete": len(toDelete), "groups": toDelete})
		}
		fmt.Fprintf(os.Stderr, "Would delete %d group(s):\n", len(toDelete))
		for _, g := range toDelete {
			name := g.Name
			if name == "" {
				name = g.JID
			}
			leftInfo := ""
			if !g.LeftAt.IsZero() {
				leftInfo = " (left)"
			}
			fmt.Fprintf(os.Stderr, "  - %s (%s)%s\n", name, g.JID, leftInfo)
		}
		fmt.Fprintln(os.Stderr, "\nRun without --dry-run to actually delete.")
		return nil
	}

	if !confirm {
		fmt.Fprintf(os.Stderr, "About to delete %d group(s). This cannot be undone.\n", len(toDelete))
		fmt.Fprint(os.Stderr, "Continue? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	var deleted int
	for _, g := range toDelete {
		if err := a.DB().DeleteGroup(g.JID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete group %s: %v\n", g.JID, err)
			continue
		}
		_ = a.DB().DeleteChat(g.JID)
		deleted++
		if !asJSON {
			name := g.Name
			if name == "" {
				name = g.JID
			}
			fmt.Fprintf(os.Stderr, "Deleted %s\n", name)
		}
	}

	if asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"deleted": deleted})
	}
	fmt.Fprintf(os.Stderr, "\nDone. Deleted %d group(s).\n", deleted)
	return nil
}
