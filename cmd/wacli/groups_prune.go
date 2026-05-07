package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/spf13/cobra"
)

func newGroupsPruneCmd(flags *rootFlags) *cobra.Command {
	var days int
	var leftOnly bool
	var includeActive bool
	var dryRun bool
	var confirm bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old or left groups from local storage",
		Long: `Clean up groups that you have left or that have been inactive.

By default, removes groups you have left. Use --days to prune only left
groups older than the threshold. Add --include-active to also prune active
groups whose last local message is older than the threshold.

This only deletes local wacli store rows. It does not leave WhatsApp groups
or delete anything from WhatsApp servers. Use --dry-run to preview targets.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			if days < 0 {
				return fmt.Errorf("days must not be negative")
			}
			if !leftOnly {
				includeActive = true
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			return pruneGroups(a, days, includeActive, dryRun, confirm, flags.asJSON)
		},
	}
	cmd.Flags().IntVar(&days, "days", 0, "prune groups older than N days (0 = all left groups)")
	cmd.Flags().BoolVar(&leftOnly, "left-only", true, "only remove groups you have left")
	cmd.Flags().BoolVar(&includeActive, "include-active", false, "also remove active groups with no messages in the last N days")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "skip confirmation prompt")
	return cmd
}

func pruneGroups(a *app.App, days int, includeActive, dryRun, confirm, asJSON bool) error {
	groups, err := a.DB().ListPrunableGroups(days, includeActive)
	if err != nil {
		return err
	}

	if len(groups) == 0 {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"deleted": 0, "message": "no groups to prune"})
		}
		fmt.Fprintln(os.Stderr, "No groups to prune.")
		return nil
	}

	if dryRun {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"would_delete": len(groups), "groups": groups})
		}
		writePruneTargets(os.Stderr, "Would delete", groups)
		fmt.Fprintln(os.Stderr, "\nRun without --dry-run to actually delete.")
		return nil
	}

	if !confirm {
		fmt.Fprintf(os.Stderr, "About to delete %d group(s) from the local wacli store. This cannot be undone.\n", len(groups))
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
	for _, g := range groups {
		if err := a.DB().DeleteGroupLocalData(g.JID); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to delete group %s: %v\n", g.JID, err)
			continue
		}
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

func writePruneTargets(w *os.File, prefix string, groups []store.Group) {
	fmt.Fprintf(w, "%s %d group(s):\n", prefix, len(groups))
	for _, g := range groups {
		name := g.Name
		if name == "" {
			name = g.JID
		}
		state := "left"
		if g.LeftAt.IsZero() {
			state = "inactive"
		}
		fmt.Fprintf(w, "  - %s (%s, %s)\n", name, g.JID, state)
	}
}
