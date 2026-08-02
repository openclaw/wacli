package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

func newContactsCheckCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "check <phone> [phone...]",
		Short: "Check live whether phone numbers are registered on WhatsApp",
		Long: "Query WhatsApp's servers whether each phone number is registered.\n" +
			"Accepts +E164 numbers, common formatting, or user JIDs.\n" +
			"Connects with the account session; results are not stored locally.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runContactsCheck(flags, args)
		},
	}
}

type registrationChecker interface {
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
}

type contactCheckResult struct {
	Query      string `json:"query"`
	Phone      string `json:"phone"`
	JID        string `json:"jid,omitempty"`
	Registered bool   `json:"registered"`
}

func checkRegistrations(ctx context.Context, checker registrationChecker, inputs []string) ([]contactCheckResult, error) {
	results := make([]contactCheckResult, 0, len(inputs))
	phones := make([]string, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		jid, err := wa.ParseUserOrJID(in)
		if err != nil {
			return nil, err
		}
		if jid.Server != types.DefaultUserServer {
			return nil, fmt.Errorf("unsupported recipient %q: pass a phone number or user JID", in)
		}
		if !seen[jid.User] {
			seen[jid.User] = true
			phones = append(phones, "+"+jid.User)
		}
		results = append(results, contactCheckResult{Query: in, Phone: jid.User})
	}

	resps, err := checker.IsOnWhatsApp(ctx, phones)
	if err != nil {
		return nil, err
	}
	byQuery := make(map[string]types.IsOnWhatsAppResponse, len(resps))
	for _, r := range resps {
		byQuery[r.Query] = r
	}
	for i := range results {
		r, ok := byQuery["+"+results[i].Phone]
		if !ok {
			continue
		}
		results[i].Registered = r.IsIn
		if r.IsIn && !r.JID.IsEmpty() {
			results[i].JID = r.JID.ToNonAD().String()
		}
	}
	return results, nil
}

func runContactsCheck(flags *rootFlags, args []string) error {
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

	if err := a.EnsureAuthed(); err != nil {
		return err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return err
	}

	results, err := checkRegistrations(ctx, a.WA(), args)
	if err != nil {
		return err
	}

	if flags.asJSON {
		return out.WriteJSON(os.Stdout, results)
	}
	for _, r := range results {
		status := "not registered"
		if r.Registered {
			status = "registered"
			if r.JID != "" && r.JID != r.Phone+"@s.whatsapp.net" {
				status += " (" + r.JID + ")"
			}
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\n", r.Phone, status)
	}
	return nil
}
