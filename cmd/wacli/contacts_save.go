package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/app"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

const contactsSaveKind = "contacts_save"

// contactSaveResult is the command's JSON output. A running sync --follow
// process returns the same shape when it saves on the caller's behalf.
type contactSaveResult struct {
	JID       string `json:"jid"`
	Name      string `json:"name"`
	FirstName string `json:"first_name"`
	Phonebook bool   `json:"phonebook"`
	LID       string `json:"lid"`
}

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
			target, name, firstName, err := parseContactSaveInput(targetRaw, name, firstName)
			if err != nil {
				return err
			}
			return runContactsSave(flags, target, name, firstName, phonebook)
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "phone number or phone JID (LIDs are not accepted)")
	cmd.Flags().StringVar(&name, "name", "", "full contact name")
	cmd.Flags().StringVar(&firstName, "first-name", "", "first name (default: first word of --name)")
	cmd.Flags().BoolVar(&phonebook, "phonebook", false, "also save it to the phone's address book")
	return cmd
}

func runContactsSave(flags *rootFlags, target types.JID, name, firstName string, phonebook bool) error {
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		// A same-store `sync --follow` owns the lock and the connected
		// session; it saves on our behalf like the `chats` state commands.
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, sendDelegateRequest{
			Kind:      contactsSaveKind,
			To:        target.String(),
			Name:      name,
			FirstName: firstName,
			Phonebook: phonebook,
		})
		if delegated {
			if delegateErr != nil {
				return explainContactsSaveDelegateError(delegateErr)
			}
			if resp.Contact == nil {
				return fmt.Errorf("the running sync process did not report the saved contact for %s; restart `wacli sync` after upgrading", target.User)
			}
			return writeContactSaveResult(flags, *resp.Contact)
		}
		return err
	}
	defer closeApp(a, lk)

	if err := a.EnsureAuthed(ctx); err != nil {
		return err
	}
	// whatsmeow fetches the collection after the send and emits the echo of
	// our mutation (and anything the phone changed meanwhile) as events;
	// persist them like the `chats` state commands do instead of leaving
	// wacli.db behind the session store.
	removePersistenceHandler, err := a.AddChatStatePersistenceHandler(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Keep the handler active until the socket is closed, then let App.Close
		// drain every persistence task before it closes the local database.
		a.WA().Disconnect()
		removePersistenceHandler()
	}()
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}
	result, err := saveContact(ctx, a, target, name, firstName, phonebook)
	if err != nil {
		return err
	}
	return writeContactSaveResult(flags, result)
}

// contactSaveApp is the part of the app a save needs, so the delegated path
// can be tested without a live session.
type contactSaveApp interface {
	SaveContact(ctx context.Context, target types.JID, fullName, firstName string, phonebook bool) (app.SavedContact, error)
}

func saveContact(ctx context.Context, a contactSaveApp, target types.JID, name, firstName string, phonebook bool) (contactSaveResult, error) {
	saved, err := a.SaveContact(ctx, target, name, firstName, phonebook)
	if err != nil {
		return contactSaveResult{}, err
	}
	return contactSaveResult{
		JID:       saved.JID.String(),
		Name:      saved.FullName,
		FirstName: saved.FirstName,
		Phonebook: saved.Phonebook,
		LID:       saved.LID.String(),
	}, nil
}

// executeDelegatedContactsSave runs the save on the follow process, which
// owns the store lock and the connected session. It validates the request
// like the command does, so an older or hand-written client gets the same errors.
func executeDelegatedContactsSave(ctx context.Context, a contactSaveApp, req sendDelegateRequest) (sendDelegateResponse, error) {
	target, name, firstName, err := parseContactSaveInput(req.To, req.Name, req.FirstName)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	result, err := saveContact(ctx, a, target, name, firstName, req.Phonebook)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	return sendDelegateResponse{OK: true, Contact: &result}, nil
}

// explainContactsSaveDelegateError turns an older daemon's rejection into the action to take.
func explainContactsSaveDelegateError(err error) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), contactsSaveKind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support contacts save; restart `wacli sync` after upgrading, then run this again: %w", err)
}

func writeContactSaveResult(flags *rootFlags, result contactSaveResult) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, result)
	}
	jid, _ := types.ParseJID(result.JID)
	fmt.Fprintf(os.Stderr, "Contact saved: %s (%s)\n", sanitize(result.Name), jid.User)
	return nil
}

// parseContactSaveInput validates the target and names before any lock,
// connection, or delegation, so mistakes fail fast with the same errors in
// both paths.
func parseContactSaveInput(targetRaw, name, firstName string) (types.JID, string, string, error) {
	target, err := parseContactSaveTarget(targetRaw)
	if err != nil {
		return types.JID{}, "", "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return types.JID{}, "", "", fmt.Errorf("--name is required")
	}
	if err := wa.ValidateContactName(name); err != nil {
		return types.JID{}, "", "", fmt.Errorf("--name: %w", err)
	}
	firstName = strings.TrimSpace(firstName)
	if err := wa.ValidateContactName(firstName); err != nil {
		return types.JID{}, "", "", fmt.Errorf("--first-name: %w", err)
	}
	return target, name, firstName, nil
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
	// A full JID skips the bare-number normalization, so check its user part
	// here rather than after connecting.
	if target.User == "" || strings.Trim(target.User, "0123456789") != "" {
		return types.JID{}, fmt.Errorf("--jid must be a phone number JID with a numeric user part, got %q", raw)
	}
	return target.ToNonAD(), nil
}
