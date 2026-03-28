package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/steipete/wacli/internal/config"
	"github.com/steipete/wacli/internal/out"
)

func newAccountsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "accounts",
		Aliases: []string{"account"},
		Short:   "Manage WhatsApp accounts",
	}

	cmd.AddCommand(newAccountsListCmd(flags))
	cmd.AddCommand(newAccountsDefaultCmd(flags))
	cmd.AddCommand(newAccountsRemoveCmd(flags))

	return cmd
}

func newAccountsListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := flags.storeDir
			if baseDir == "" {
				baseDir = config.DefaultStoreDir()
			}
			baseDir, _ = filepath.Abs(baseDir)

			if err := config.MaybeMigrateLegacyStore(baseDir); err != nil {
				return err
			}

			accounts, err := config.ListAccounts(baseDir)
			if err != nil {
				return fmt.Errorf("list accounts: %w", err)
			}

			defaultAccount := config.ReadDefaultAccount(baseDir)

			if flags.asJSON {
				type entry struct {
					Name    string `json:"name"`
					Default bool   `json:"default"`
				}
				var entries []entry
				for _, name := range accounts {
					entries = append(entries, entry{Name: name, Default: name == defaultAccount})
				}
				return out.WriteJSON(os.Stdout, entries)
			}

			if len(accounts) == 0 {
				fmt.Fprintln(os.Stdout, "No accounts found. Run `wacli auth` to set one up.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
			for _, name := range accounts {
				marker := ""
				if name == defaultAccount {
					marker = " (default)"
				}
				fmt.Fprintf(w, "%s%s\n", name, marker)
			}
			return w.Flush()
		},
	}
}

func newAccountsDefaultCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "default [name]",
		Short: "Get or set the default account",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := flags.storeDir
			if baseDir == "" {
				baseDir = config.DefaultStoreDir()
			}
			baseDir, _ = filepath.Abs(baseDir)

			if len(args) == 0 {
				name := config.ReadDefaultAccount(baseDir)
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]string{"default": name})
				}
				fmt.Fprintln(os.Stdout, name)
				return nil
			}

			name := args[0]
			if err := config.ValidateAccountName(name); err != nil {
				return err
			}

			// Verify account exists
			acctDir := config.AccountDir(baseDir, name)
			if _, err := os.Stat(acctDir); os.IsNotExist(err) {
				return fmt.Errorf("account %q does not exist", name)
			}

			if err := config.WriteDefaultAccount(baseDir, name); err != nil {
				return fmt.Errorf("set default account: %w", err)
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]string{"default": name})
			}
			fmt.Fprintf(os.Stdout, "Default account set to %q.\n", name)
			return nil
		},
	}
}

func newAccountsRemoveCmd(flags *rootFlags) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove an account and its data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := config.ValidateAccountName(name); err != nil {
				return err
			}

			baseDir := flags.storeDir
			if baseDir == "" {
				baseDir = config.DefaultStoreDir()
			}
			baseDir, _ = filepath.Abs(baseDir)

			acctDir := config.AccountDir(baseDir, name)
			if _, err := os.Stat(acctDir); os.IsNotExist(err) {
				return fmt.Errorf("account %q does not exist", name)
			}

			if !force {
				return fmt.Errorf("refusing to remove account %q without --force (this deletes all local data for this account)", name)
			}

			if err := os.RemoveAll(acctDir); err != nil {
				return fmt.Errorf("remove account: %w", err)
			}

			// If we removed the default, clear the default file
			if config.ReadDefaultAccount(baseDir) == name {
				_ = os.Remove(filepath.Join(baseDir, "default_account"))
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"removed": name})
			}
			fmt.Fprintf(os.Stdout, "Account %q removed.\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion of account data")
	return cmd
}
