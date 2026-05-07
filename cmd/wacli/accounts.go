package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/openclaw/wacli/internal/config"
	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
)

type accountPayload struct {
	Name            string `json:"name"`
	Label           string `json:"label,omitempty"`
	ConfiguredStore string `json:"configured_store"`
	StoreDir        string `json:"store_dir"`
	Default         bool   `json:"default"`
}

func newAccountsCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accounts",
		Short: "Manage named WhatsApp accounts",
	}
	cmd.AddCommand(newAccountsListCmd(flags))
	cmd.AddCommand(newAccountsAddCmd(flags))
	cmd.AddCommand(newAccountsUseCmd(flags))
	cmd.AddCommand(newAccountsShowCmd(flags))
	cmd.AddCommand(newAccountsRemoveCmd(flags))
	return cmd
}

func newAccountsListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured accounts",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.DefaultConfigPath()
			cfg, _, err := config.LoadAccountsConfigIfExists(path)
			if err != nil {
				return err
			}
			accounts := sortedAccounts(path, cfg)
			payloads := accountPayloads(accounts)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"config_path":     path,
					"default_account": cfg.DefaultAccount,
					"accounts":        payloads,
				})
			}
			if len(accounts) == 0 {
				fmt.Fprintln(os.Stdout, "No accounts configured. Run `wacli accounts add personal`.")
				return nil
			}
			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "DEFAULT\tNAME\tSTORE")
			for _, account := range accounts {
				mark := ""
				if account.Default {
					mark = "*"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", mark, account.Name, account.StoreDir)
			}
			_ = w.Flush()
			return nil
		},
	}
}

func newAccountsAddCmd(flags *rootFlags) *cobra.Command {
	opts := authOptions{idleExit: 30 * time.Second, qrFormat: "terminal"}
	var noAuth bool
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add an account and authenticate it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			name := args[0]
			if err := config.ValidateAccountName(name); err != nil {
				return err
			}
			if !noAuth {
				if _, err := validateAuthOptions(flags, opts); err != nil {
					return err
				}
			}
			path := config.DefaultConfigPath()
			cfg, _, err := config.LoadAccountsConfigIfExists(path)
			if err != nil {
				return err
			}
			if _, ok := cfg.Accounts[name]; ok {
				return fmt.Errorf("account %q already exists", name)
			}
			cfg.Accounts[name] = config.AccountEntry{Store: config.DefaultAccountStore(name)}
			if cfg.DefaultAccount == "" {
				cfg.DefaultAccount = name
			}
			storeDir := config.ListAccounts(path, cfg)
			var added config.Account
			for _, account := range storeDir {
				if account.Name == name {
					added = account
					break
				}
			}
			if err := fsutil.EnsurePrivateDir(added.StoreDir); err != nil {
				return fmt.Errorf("create account store: %w", err)
			}
			if err := config.SaveAccountsConfig(path, cfg); err != nil {
				return err
			}
			if noAuth {
				if flags.asJSON {
					return out.WriteJSON(os.Stdout, map[string]any{
						"config_path": path,
						"account":     accountPayloadFromAccount(added),
					})
				}
				fmt.Fprintf(os.Stdout, "Account %s added at %s. Run `wacli --account %s auth` to authenticate.\n", name, added.StoreDir, name)
				return nil
			}

			oldAccount := flags.account
			oldStore := flags.storeDir
			flags.account = name
			flags.storeDir = ""
			defer func() {
				flags.account = oldAccount
				flags.storeDir = oldStore
			}()

			if !flags.asJSON {
				fmt.Fprintf(os.Stdout, "Account %s added at %s\n", name, added.StoreDir)
			}
			res, err := runAuth(flags, opts)
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"account":         accountPayloadFromAccount(added),
					"authenticated":   true,
					"messages_stored": res.MessagesStored,
				})
			}
			fmt.Fprintf(os.Stdout, "Account %s authenticated. Messages stored: %d\n", name, res.MessagesStored)
			return nil
		},
	}
	addAuthFlags(cmd, &opts)
	cmd.Flags().BoolVar(&noAuth, "no-auth", false, "create the account without running auth")
	return cmd
}

func newAccountsUseCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "use NAME",
		Short: "Set the default account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			name := args[0]
			if err := config.ValidateAccountName(name); err != nil {
				return err
			}
			path := config.DefaultConfigPath()
			cfg, err := config.LoadAccountsConfig(path)
			if err != nil {
				return err
			}
			if _, ok := cfg.Accounts[name]; !ok {
				return fmt.Errorf("account %q is not configured", name)
			}
			cfg.DefaultAccount = name
			if err := config.SaveAccountsConfig(path, cfg); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{"default_account": name})
			}
			fmt.Fprintf(os.Stdout, "Default account: %s\n", name)
			return nil
		},
	}
}

func newAccountsShowCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "show NAME",
		Short: "Show one configured account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, account, err := config.ResolveAccountStore(config.DefaultConfigPath(), args[0])
			if err != nil {
				return err
			}
			payload := accountPayloadFromAccount(account)
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, payload)
			}
			fmt.Fprintf(os.Stdout, "Name: %s\nStore: %s\nDefault: %t\n", payload.Name, payload.StoreDir, payload.Default)
			if payload.Label != "" {
				fmt.Fprintf(os.Stdout, "Label: %s\n", payload.Label)
			}
			return nil
		},
	}
}

func newAccountsRemoveCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove an account from config without deleting its store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			name := args[0]
			if err := config.ValidateAccountName(name); err != nil {
				return err
			}
			path := config.DefaultConfigPath()
			cfg, err := config.LoadAccountsConfig(path)
			if err != nil {
				return err
			}
			entry, ok := cfg.Accounts[name]
			if !ok {
				return fmt.Errorf("account %q is not configured", name)
			}
			storeDir := config.ListAccounts(path, &config.AccountsConfig{
				DefaultAccount: cfg.DefaultAccount,
				Accounts:       map[string]config.AccountEntry{name: entry},
			})[0].StoreDir
			delete(cfg.Accounts, name)
			if cfg.DefaultAccount == name {
				cfg.DefaultAccount = ""
			}
			if err := config.SaveAccountsConfig(path, cfg); err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"removed":        name,
					"store_dir_kept": storeDir,
				})
			}
			fmt.Fprintf(os.Stdout, "Removed account %s. Store kept at %s\n", name, storeDir)
			return nil
		},
	}
}

func sortedAccounts(path string, cfg *config.AccountsConfig) []config.Account {
	accounts := config.ListAccounts(path, cfg)
	sort.Slice(accounts, func(i, j int) bool {
		return accounts[i].Name < accounts[j].Name
	})
	return accounts
}

func accountPayloads(accounts []config.Account) []accountPayload {
	payloads := make([]accountPayload, 0, len(accounts))
	for _, account := range accounts {
		payloads = append(payloads, accountPayloadFromAccount(account))
	}
	return payloads
}

func accountPayloadFromAccount(account config.Account) accountPayload {
	return accountPayload{
		Name:            account.Name,
		Label:           account.Label,
		ConfiguredStore: account.ConfiguredStore,
		StoreDir:        account.StoreDir,
		Default:         account.Default,
	}
}
