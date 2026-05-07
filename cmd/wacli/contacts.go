package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
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

			fullOutput := fullTableOutput(flags.fullOutput)
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "ALIAS\tNAME\tPHONE\tJID")
			for _, c := range cs {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					tableCell(c.Alias, 18, fullOutput),
					tableCell(c.Name, 24, fullOutput),
					tableCell(c.Phone, 14, fullOutput),
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
			if err := flags.requireWritable(); err != nil {
				return err
			}
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
				jid = canonicalCLIJID(jid)
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
			if err := flags.requireWritable(); err != nil {
				return err
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
			if err := flags.requireWritable(); err != nil {
				return err
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
			if err := flags.requireWritable(); err != nil {
				return err
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
			if err := flags.requireWritable(); err != nil {
				return err
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
