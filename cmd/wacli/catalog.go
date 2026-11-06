package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

const (
	catalogListDefaultLimit = 50
	catalogListMaxLimit     = 200
)

func newCatalogCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Read WhatsApp Business catalogs",
	}
	cmd.AddCommand(newCatalogListCmd(flags))
	return cmd
}

func newCatalogListCmd(flags *rootFlags) *cobra.Command {
	var targetRaw, after string
	var limit int
	cmd := &cobra.Command{
		Use:   "list [--jid <jid-or-phone>] [--limit N] [--after <cursor>]",
		Short: "List catalog products (read-only; defaults to this account)",
		Long: `List the products of a WhatsApp Business catalog: id, name, price, sale
price, currency, image URLs, review status and whether the product is hidden.
Prices come as WhatsApp sends them (thousandths of the currency unit) plus a
two-decimal rendering.

WhatsApp no longer answers the catalog IQ over the socket, so this uses the
same public HTTPS query WhatsApp Web uses (graph.whatsapp.com). It needs no
session and never touches the running sync: --jid names any business, and
without it the linked account's JID is read from the store (works with
--read-only). "has_catalog": false means WhatsApp knows no catalog for it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			limit, err := validateCatalogLimit(limit)
			if err != nil {
				return err
			}
			var jid types.JID
			if strings.TrimSpace(targetRaw) != "" {
				jid, err = wa.ParseUserOrJID(strings.TrimSpace(targetRaw))
				if err != nil {
					return err
				}
			} else {
				ctx, cancel := withTimeout(context.Background(), flags)
				a, lk, err := newApp(ctx, flags, false, false)
				if err != nil {
					cancel()
					return err
				}
				linked := a.WA().LinkedJID()
				closeApp(a, lk)
				cancel()
				if linked == "" {
					return fmt.Errorf("account is not linked; pass --jid")
				}
				jid, err = types.ParseJID(linked)
				if err != nil {
					return err
				}
			}
			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()
			page, err := (&wa.CatalogClient{}).Fetch(ctx, jid, limit, strings.TrimSpace(after))
			if err != nil {
				return fmt.Errorf("get catalog: %w", err)
			}
			return writeCatalogPage(flags, page)
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "business JID or phone number (default: this account)")
	cmd.Flags().IntVar(&limit, "limit", catalogListDefaultLimit, fmt.Sprintf("products per page (1-%d)", catalogListMaxLimit))
	cmd.Flags().StringVar(&after, "after", "", "cursor from a previous page (next_cursor)")
	return cmd
}

func validateCatalogLimit(limit int) (int, error) {
	if limit == 0 {
		return catalogListDefaultLimit, nil
	}
	if limit < 1 || limit > catalogListMaxLimit {
		return 0, fmt.Errorf("--limit must be between 1 and %d", catalogListMaxLimit)
	}
	return limit, nil
}

func writeCatalogPage(flags *rootFlags, page *wa.CatalogPage) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, page)
	}
	if !page.HasCatalog {
		fmt.Fprintf(os.Stdout, "%s has no catalog.\n", sanitize(page.JID))
		return nil
	}
	w := newTableWriter(os.Stdout)
	fmt.Fprintln(w, "ID\tNAME\tPRICE\tSALE\tCURRENCY\tHIDDEN\tSTATUS")
	for _, p := range page.Products {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%t\t%s\n", sanitize(p.ID), sanitize(p.Name), p.Price, p.SalePrice, sanitize(p.Currency), p.Hidden, sanitize(p.ReviewStatus))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if page.NextCursor != "" {
		fmt.Fprintf(os.Stdout, "More products: --after %s\n", sanitize(page.NextCursor))
	}
	return nil
}
