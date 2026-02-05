package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/contacts"
	"github.com/steipete/wacli/internal/out"
)

func newContactsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contacts",
		Short: "Search and manage local contact metadata",
	}
	cmd.AddCommand(newContactsSearchCmd(flags))
	cmd.AddCommand(newContactsShowCmd(flags))
	cmd.AddCommand(newContactsRefreshCmd(flags))
	cmd.AddCommand(newContactsImportSystemCmd(flags))
	cmd.AddCommand(newContactsAliasCmd(flags))
	cmd.AddCommand(newContactsTagsCmd(flags))
	return cmd
}

func newContactsSearchCmd(flags *rootFlags) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search contacts (from synced metadata)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			cs, err := a.DB().SearchContacts(args[0], limit)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, cs)
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ALIAS\tNAME\tPHONE\tJID")
			for _, c := range cs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					truncate(c.Alias, 18),
					truncate(c.Name, 24),
					truncate(c.Phone, 14),
					c.JID,
				)
			}
			_ = w.Flush()
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "limit results")
	return cmd
}

func newContactsShowCmd(flags *rootFlags) *cobra.Command {
	var jid string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show one contact",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(jid) == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			c, err := a.DB().GetContact(jid)
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, c)
			}

			fmt.Fprintf(os.Stdout, "JID: %s\n", c.JID)
			if c.Phone != "" {
				fmt.Fprintf(os.Stdout, "Phone: %s\n", c.Phone)
			}
			if c.Name != "" {
				fmt.Fprintf(os.Stdout, "Name: %s\n", c.Name)
			}
			if c.Alias != "" {
				fmt.Fprintf(os.Stdout, "Alias: %s\n", c.Alias)
			}
			if c.SystemName != "" {
				fmt.Fprintf(os.Stdout, "System Name: %s\n", c.SystemName)
			}
			if len(c.Tags) > 0 {
				fmt.Fprintf(os.Stdout, "Tags: %s\n", strings.Join(c.Tags, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jid, "jid", "", "contact JID")
	return cmd
}

func newContactsRefreshCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Import contacts from whatsmeow store into local DB",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, true, true)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.OpenWA(); err != nil {
				return err
			}
			cs, err := a.WA().GetAllContacts(ctx)
			if err != nil {
				return err
			}

			var count int
			for jid, info := range cs {
				_ = a.DB().UpsertContact(
					jid.String(),
					jid.User,
					info.PushName,
					info.FullName,
					info.FirstName,
					info.BusinessName,
				)
				count++
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"contacts": count})
			}
			fmt.Fprintf(os.Stdout, "Imported %d contacts.\n", count)
			return nil
		},
	}
	return cmd
}

func newContactsImportSystemCmd(flags *rootFlags) *cobra.Command {
	var dryRun bool
	var clear bool
	cmd := &cobra.Command{
		Use:   "import-system",
		Short: "Import names from macOS Contacts (matches by phone number)",
		Long: `Imports contacts from macOS Contacts.app and sets them as system names in wacli.

This command:
1. Reads all contacts with phone numbers from macOS Contacts.app
2. Matches them against existing wacli contacts by phone number
3. Sets the system contact name (displayed with precedence over WhatsApp names)

Display name precedence:
  1. Manual alias (user override - highest priority)
  2. System contact name (from this import)
  3. WhatsApp names (push_name, full_name, etc.)
  4. Phone number (fallback)

This mirrors how the native WhatsApp app displays contacts.

Run 'wacli contacts refresh' first to ensure contacts are synced from WhatsApp.

Note: Only supported on macOS.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			// Open DB
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			// Handle --clear flag
			if clear {
				if dryRun {
					count, err := a.DB().CountSystemNames()
					if err != nil {
						return fmt.Errorf("count system names: %w", err)
					}
					if flags.asJSON {
						return out.WriteJSON(os.Stdout, map[string]any{
							"action":  "clear",
							"count":   count,
							"dryRun":  true,
						})
					}
					fmt.Fprintf(os.Stdout, "[DRY RUN] Would clear %d system names.\n", count)
					return nil
				}
				count, err := a.DB().ClearAllSystemNames()
				if err != nil {
					return fmt.Errorf("clear system names: %w", err)
				}
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"action":  "clear",
						"cleared": count,
					})
				}
				fmt.Fprintf(os.Stdout, "Cleared %d system names.\n", count)
				return nil
			}

			// Fetch system contacts
			fmt.Fprintln(os.Stderr, "Reading macOS Contacts...")
			systemContacts, err := contacts.GetSystemContacts()
			if err != nil {
				return fmt.Errorf("failed to read system contacts: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Found %d contacts with phone numbers\n", len(systemContacts))

			// Build phone-to-name map
			phoneToName := contacts.BuildPhoneToNameMap(systemContacts)
			fmt.Fprintf(os.Stderr, "Built lookup map with %d unique phone numbers\n", len(phoneToName))

			// Get all contacts from wacli DB
			allContacts, err := a.DB().SearchContacts("%", 100000)
			if err != nil {
				return fmt.Errorf("failed to list contacts: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Checking %d wacli contacts...\n", len(allContacts))

			type match struct {
				JID            string `json:"jid"`
				Phone          string `json:"phone"`
				CurrentName    string `json:"currentName"`
				NewSystemName  string `json:"newSystemName"`
				HadSystemName  bool   `json:"hadSystemName"`
			}
			var matches []match
			var skippedSameName int
			var skippedNoMatch int

			for _, c := range allContacts {
				// Normalize the phone from wacli contact
				normalizedPhone := contacts.NormalizePhone(c.Phone)
				if normalizedPhone == "" {
					skippedNoMatch++
					continue
				}

				// Try to find a match in system contacts
				systemName, found := phoneToName[normalizedPhone]
				if !found {
					skippedNoMatch++
					continue
				}

				// Skip if system name is already the same
				if c.SystemName == systemName {
					skippedSameName++
					continue
				}

				matches = append(matches, match{
					JID:           c.JID,
					Phone:         c.Phone,
					CurrentName:   c.Name,
					NewSystemName: systemName,
					HadSystemName: c.SystemName != "",
				})
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"matches":         matches,
					"skippedSameName": skippedSameName,
					"skippedNoMatch":  skippedNoMatch,
					"dryRun":          dryRun,
				})
			}

			fmt.Fprintf(os.Stderr, "\nResults:\n")
			fmt.Fprintf(os.Stderr, "  Matched: %d\n", len(matches))
			fmt.Fprintf(os.Stderr, "  Skipped (same name): %d\n", skippedSameName)
			fmt.Fprintf(os.Stderr, "  Skipped (no phone match): %d\n", skippedNoMatch)

			if len(matches) == 0 {
				fmt.Fprintln(os.Stdout, "\nNo new matches to import.")
				return nil
			}

			if dryRun {
				fmt.Fprintln(os.Stdout, "\n[DRY RUN] Would set these system names:")
				w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
				fmt.Fprintln(w, "PHONE\tCURRENT\tSYSTEM NAME")
				for _, m := range matches {
					fmt.Fprintf(w, "%s\t%s\t%s\n",
						truncate(m.Phone, 16),
						truncate(m.CurrentName, 20),
						truncate(m.NewSystemName, 24),
					)
				}
				_ = w.Flush()
				fmt.Fprintln(os.Stdout, "\nRun without --dry-run to apply.")
				return nil
			}

			// Apply system names
			fmt.Fprintln(os.Stdout, "\nSetting system names...")
			var applied int
			for _, m := range matches {
				if err := a.DB().SetSystemName(m.JID, m.NewSystemName); err != nil {
					fmt.Fprintf(os.Stderr, "  Failed to set system name for %s: %v\n", m.JID, err)
					continue
				}
				applied++
			}
			fmt.Fprintf(os.Stdout, "Applied %d system names.\n", applied)

			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be imported without making changes")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove all imported system names")
	return cmd
}

func newContactsAliasCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Manage local aliases",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set",
		Short: "Set alias",
		RunE: func(cmd *cobra.Command, args []string) error {
			jid, _ := cmd.Flags().GetString("jid")
			alias, _ := cmd.Flags().GetString("alias")
			if strings.TrimSpace(jid) == "" || strings.TrimSpace(alias) == "" {
				return fmt.Errorf("--jid and --alias are required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.DB().SetAlias(jid, alias); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid, "alias": alias})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rm",
		Short: "Remove alias",
		RunE: func(cmd *cobra.Command, args []string) error {
			jid, _ := cmd.Flags().GetString("jid")
			if strings.TrimSpace(jid) == "" {
				return fmt.Errorf("--jid is required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.DB().RemoveAlias(jid); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid, "removed": true})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	})

	_ = cmd.PersistentFlags().String("jid", "", "contact JID")
	_ = cmd.PersistentFlags().String("alias", "", "alias")
	return cmd
}

func newContactsTagsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tags",
		Short: "Manage local tags",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "add",
		Short: "Add tag",
		RunE: func(cmd *cobra.Command, args []string) error {
			jid, _ := cmd.Flags().GetString("jid")
			tag, _ := cmd.Flags().GetString("tag")
			if strings.TrimSpace(jid) == "" || strings.TrimSpace(tag) == "" {
				return fmt.Errorf("--jid and --tag are required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.DB().AddTag(jid, tag); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid, "tag": tag})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "rm",
		Short: "Remove tag",
		RunE: func(cmd *cobra.Command, args []string) error {
			jid, _ := cmd.Flags().GetString("jid")
			tag, _ := cmd.Flags().GetString("tag")
			if strings.TrimSpace(jid) == "" || strings.TrimSpace(tag) == "" {
				return fmt.Errorf("--jid and --tag are required")
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)
			if err := a.DB().RemoveTag(jid, tag); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"jid": jid, "tag": tag, "removed": true})
			}
			fmt.Fprintln(os.Stdout, "OK")
			return nil
		},
	})

	_ = cmd.PersistentFlags().String("jid", "", "contact JID")
	_ = cmd.PersistentFlags().String("tag", "", "tag")
	return cmd
}
