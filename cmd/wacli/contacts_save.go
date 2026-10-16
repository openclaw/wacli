package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newContactsSaveCmd(flags *rootFlags) *cobra.Command {
	var targetRaw, name, firstName string
	var phonebook bool
	cmd := &cobra.Command{
		Use:   "save --jid PHONE_OR_JID --name NAME",
		Short: "Save or rename a contact in your WhatsApp account",
		Long: "Saves the contact name to your WhatsApp account, like \"Save contact\" in the app. " +
			"It syncs to your phone and linked devices. Running it again renames the contact. " +
			"With --phonebook the phone also writes it to the device address book.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := flags.requireWritable(); err != nil {
				return err
			}
			target, err := parseContactSaveTarget(targetRaw)
			if err != nil {
				return err
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			ctx, cancel, a, lk, err := openLiveProfileApp(flags, true)
			if err != nil {
				return err
			}
			defer cancel()
			defer closeApp(a, lk)
			// Current clients key contacts by LID and show only the number for
			// a contact saved without it, so refuse rather than half-save.
			lid := a.WA().ResolvePNToLID(ctx, target)
			if lid.Server != types.HiddenUserServer || lid.User == "" {
				return fmt.Errorf("could not find the WhatsApp ID (LID) of %s; nothing was saved (is the number on WhatsApp?)", target.User)
			}
			patch, err := wa.BuildSaveContact(target, lid, name, firstName, phonebook)
			if err != nil {
				return err
			}
			first := patch.Mutations[0].Value.GetContactAction().GetFirstName()
			if err := a.WA().SaveContact(ctx, target, lid, name, first, phonebook); err != nil {
				return fmt.Errorf("save contact: %w", err)
			}
			if err := a.DB().UpsertContact(target.String(), target.User, "", name, first, ""); err != nil {
				return fmt.Errorf("contact saved on WhatsApp, but updating the local index failed: %w", err)
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"jid":        target.String(),
					"name":       name,
					"first_name": first,
					"phonebook":  phonebook,
					"lid":        patch.Mutations[0].Value.GetContactAction().GetLidJID(),
				})
			}
			fmt.Fprintf(os.Stderr, "Contact saved: %s (%s)\n", sanitize(name), target.User)
			return nil
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "phone number or phone JID (LIDs are not accepted)")
	cmd.Flags().StringVar(&name, "name", "", "full contact name")
	cmd.Flags().StringVar(&firstName, "first-name", "", "first name (default: first word of --name)")
	cmd.Flags().BoolVar(&phonebook, "phonebook", false, "also save it to the phone's address book")
	return cmd
}

func parseContactSaveTarget(raw string) (types.JID, error) {
	if strings.TrimSpace(raw) == "" {
		return types.JID{}, fmt.Errorf("--jid is required")
	}
	target, err := wa.ParseUserOrJID(raw)
	if err != nil {
		return types.JID{}, err
	}
	if target.Server != types.DefaultUserServer {
		return types.JID{}, fmt.Errorf("--jid must be a phone number (got %s); use `wacli contacts resolve` to find the number of a LID", target)
	}
	return target.ToNonAD(), nil
}
