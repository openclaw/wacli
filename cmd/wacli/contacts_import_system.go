package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/syscontacts"
	"github.com/spf13/cobra"
)

type systemContactMatch struct {
	JID           string `json:"jid"`
	Phone         string `json:"phone"`
	CurrentName   string `json:"current_name"`
	SystemName    string `json:"system_name"`
	ExistingValue string `json:"existing_system_name,omitempty"`
}

func newContactsImportSystemCmd(flags *rootFlags) *cobra.Command {
	var dryRun bool
	var clear bool
	var input string
	cmd := &cobra.Command{
		Use:   "import-system",
		Short: "Import display names from macOS Contacts",
		Long: `Import display names from macOS Contacts and store them as local system names.

System names are local wacli metadata. They do not edit WhatsApp contacts or
macOS Contacts. Display precedence is: alias, system name, WhatsApp names.

On macOS, the default source is the Contacts framework. Use --input to import
from a JSON array or NDJSON file with fields first_name, last_name, full_name,
and phones.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun {
				if err := flags.requireWritable(); err != nil {
					return err
				}
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, !dryRun, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if clear {
				return runContactsSystemClear(a.DB(), dryRun, flags.asJSON)
			}

			systemContacts, err := readSystemContacts(ctx, input)
			if err != nil {
				return err
			}
			phoneToName := syscontacts.PhoneToName(systemContacts)
			localContacts, err := a.DB().ListContacts(0)
			if err != nil {
				return err
			}

			matches, skippedNoPhone, skippedNoMatch, skippedSame := matchSystemContacts(localContacts, phoneToName)
			result := map[string]any{
				"matched":          len(matches),
				"matches":          matches,
				"skipped_no_phone": skippedNoPhone,
				"skipped_no_match": skippedNoMatch,
				"skipped_same":     skippedSame,
				"dry_run":          dryRun,
			}

			if dryRun {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, result)
				}
				writeSystemImportPreview(matches, skippedNoPhone, skippedNoMatch, skippedSame)
				return nil
			}

			applied := 0
			for _, m := range matches {
				if err := a.DB().SetSystemName(m.JID, m.SystemName); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to set system name for %s: %v\n", m.JID, err)
					continue
				}
				applied++
			}
			result["applied"] = applied
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, result)
			}
			fmt.Fprintf(os.Stdout, "Applied %d system contact name(s).\n", applied)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be imported without writing")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear all imported system names")
	cmd.Flags().StringVar(&input, "input", "", "read system contacts from JSON/NDJSON instead of macOS Contacts")
	return cmd
}

func readSystemContacts(ctx context.Context, input string) ([]syscontacts.Contact, error) {
	if input != "" {
		return syscontacts.ReadFile(input)
	}
	return syscontacts.ReadSystem(ctx)
}

func runContactsSystemClear(db *store.DB, dryRun, asJSON bool) error {
	count, err := db.CountSystemNames()
	if err != nil {
		return err
	}
	if dryRun {
		if asJSON {
			return out.WriteJSON(os.Stdout, map[string]any{"would_clear": count, "dry_run": true})
		}
		fmt.Fprintf(os.Stdout, "Would clear %d system contact name(s).\n", count)
		return nil
	}
	cleared, err := db.ClearAllSystemNames()
	if err != nil {
		return err
	}
	if asJSON {
		return out.WriteJSON(os.Stdout, map[string]any{"cleared": cleared})
	}
	fmt.Fprintf(os.Stdout, "Cleared %d system contact name(s).\n", cleared)
	return nil
}

func matchSystemContacts(local []store.Contact, phoneToName map[string]string) ([]systemContactMatch, int, int, int) {
	var matches []systemContactMatch
	var skippedNoPhone, skippedNoMatch, skippedSame int
	for _, c := range local {
		phone := syscontacts.NormalizePhone(c.Phone)
		if phone == "" {
			skippedNoPhone++
			continue
		}
		systemName, ok := phoneToName[phone]
		if !ok {
			skippedNoMatch++
			continue
		}
		if c.SystemName == systemName {
			skippedSame++
			continue
		}
		matches = append(matches, systemContactMatch{
			JID:           c.JID,
			Phone:         c.Phone,
			CurrentName:   c.Name,
			SystemName:    systemName,
			ExistingValue: c.SystemName,
		})
	}
	return matches, skippedNoPhone, skippedNoMatch, skippedSame
}

func writeSystemImportPreview(matches []systemContactMatch, skippedNoPhone, skippedNoMatch, skippedSame int) {
	fmt.Fprintf(os.Stdout, "Would import %d system contact name(s).\n", len(matches))
	fmt.Fprintf(os.Stdout, "Skipped: %d no phone, %d no match, %d already current.\n", skippedNoPhone, skippedNoMatch, skippedSame)
	if len(matches) == 0 {
		return
	}
	w := newTableWriter(os.Stdout)
	fmt.Fprintln(w, "PHONE\tCURRENT\tSYSTEM")
	for _, m := range matches {
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			tableCell(m.Phone, 16, false),
			tableCell(m.CurrentName, 24, false),
			tableCell(m.SystemName, 24, false),
		)
	}
	_ = w.Flush()
}
