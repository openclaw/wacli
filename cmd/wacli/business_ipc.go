package main

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/wa"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// Business profile delegate kinds. When `sync --follow` holds the store lock,
// the CLI hands the IQ to that process over the send socket so the line keeps
// its single live connection; without a lock it connects itself, like the
// other profile commands. Catalogs are not IQs (see catalog.go).
const (
	businessProfileGetKind = "business_profile_get"
	businessProfileSetKind = "business_profile_set"
)

// Limits WhatsApp Business enforces on the profile (Business app and Cloud API).
const (
	businessDescriptionMaxChars = 512
	businessAddressMaxChars     = 256
	businessEmailMaxChars       = 128
	businessWebsiteMaxChars     = 256
	businessWebsitesMax         = 2
)

var businessDays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// businessProfileUpdateRequest travels inside sendDelegateRequest.BusinessProfile.
type businessProfileUpdateRequest struct {
	Description *string              `json:"description,omitempty"`
	Address     *string              `json:"address,omitempty"`
	Email       *string              `json:"email,omitempty"`
	Websites    []string             `json:"websites,omitempty"`
	WebsitesSet bool                 `json:"websites_set,omitempty"`
	Hours       *businessHoursUpdate `json:"hours,omitempty"`
}

type businessHoursUpdate struct {
	TimeZone string                `json:"timezone"`
	Days     []wa.BusinessHoursDay `json:"days"`
}

func (r businessProfileUpdateRequest) toUpdate() wa.BusinessProfileUpdate {
	update := wa.BusinessProfileUpdate{Description: r.Description, Address: r.Address, Email: r.Email}
	if r.WebsitesSet {
		update.Websites = r.Websites
		if update.Websites == nil {
			update.Websites = []string{}
		}
	}
	if r.Hours != nil {
		days := r.Hours.Days
		if days == nil {
			days = []wa.BusinessHoursDay{}
		}
		update.Hours = &wa.BusinessHoursUpdate{TimeZone: r.Hours.TimeZone, Days: days}
	}
	return update
}

type businessProfileSetResult struct {
	Updated      bool                    `json:"updated"`
	Profile      *wa.BusinessProfileFull `json:"profile"`
	ProfileError string                  `json:"profile_error,omitempty"`
}

type businessClient interface {
	LinkedJID() string
	GetBusinessProfileFull(context.Context, types.JID) (*wa.BusinessProfileFull, error)
	UpdateBusinessProfile(context.Context, wa.BusinessProfileUpdate) error
}

func businessTarget(client businessClient, raw string) (types.JID, error) {
	if strings.TrimSpace(raw) == "" {
		raw = client.LinkedJID()
		if raw == "" {
			return types.JID{}, fmt.Errorf("account is not linked")
		}
	}
	jid, err := wa.ParseUserOrJID(raw)
	if err != nil {
		return types.JID{}, err
	}
	return jid.ToNonAD(), nil
}

func executeBusinessProfileGet(ctx context.Context, client businessClient, req sendDelegateRequest) (sendDelegateResponse, error) {
	jid, err := businessTarget(client, req.JID)
	if err != nil {
		return sendDelegateResponse{}, err
	}
	profile, err := client.GetBusinessProfileFull(ctx, jid)
	if err != nil {
		return sendDelegateResponse{}, fmt.Errorf("get business profile: %w", err)
	}
	return sendDelegateResponse{OK: true, BusinessProfile: profile}, nil
}

// executeBusinessProfileSet validates again on the owner side (the request may
// come from any client), sends the delta and re-reads the profile so the
// caller sees what WhatsApp now shows.
func executeBusinessProfileSet(ctx context.Context, client businessClient, req sendDelegateRequest) (sendDelegateResponse, error) {
	if req.BusinessProfile == nil {
		return sendDelegateResponse{}, fmt.Errorf("business profile update is required")
	}
	update := req.BusinessProfile.toUpdate()
	if err := validateBusinessProfileUpdate(update); err != nil {
		return sendDelegateResponse{}, err
	}
	if err := client.UpdateBusinessProfile(ctx, update); err != nil {
		return sendDelegateResponse{}, fmt.Errorf("set business profile: %w", err)
	}
	result := &businessProfileSetResult{Updated: true}
	jid, err := businessTarget(client, "")
	if err == nil {
		result.Profile, err = client.GetBusinessProfileFull(ctx, jid)
	}
	if err != nil {
		result.Profile = nil
		result.ProfileError = err.Error()
	}
	return sendDelegateResponse{OK: true, BusinessProfileSet: result}, nil
}

// validateBusinessProfileUpdate enforces WhatsApp's field limits before
// anything reaches the wire. Messages name the field and the limit.
func validateBusinessProfileUpdate(update wa.BusinessProfileUpdate) error {
	if update.IsEmpty() {
		return fmt.Errorf("nothing to update: pass at least one of --description, --address, --email, --website, --hours")
	}
	if update.Description != nil && utf8.RuneCountInString(*update.Description) > businessDescriptionMaxChars {
		return fmt.Errorf("description exceeds %d characters", businessDescriptionMaxChars)
	}
	if update.Address != nil && utf8.RuneCountInString(*update.Address) > businessAddressMaxChars {
		return fmt.Errorf("address exceeds %d characters", businessAddressMaxChars)
	}
	if update.Email != nil && *update.Email != "" {
		if utf8.RuneCountInString(*update.Email) > businessEmailMaxChars {
			return fmt.Errorf("email exceeds %d characters", businessEmailMaxChars)
		}
		addr, err := mail.ParseAddress(*update.Email)
		if err != nil || addr.Address != *update.Email {
			return fmt.Errorf("email %q is not a plain address", *update.Email)
		}
	}
	if update.Websites != nil {
		if len(update.Websites) > businessWebsitesMax {
			return fmt.Errorf("at most %d websites are allowed", businessWebsitesMax)
		}
		for _, site := range update.Websites {
			if site == "" || strings.TrimSpace(site) != site {
				return fmt.Errorf("website %q must not be empty or padded", site)
			}
			if utf8.RuneCountInString(site) > businessWebsiteMaxChars {
				return fmt.Errorf("website exceeds %d characters", businessWebsiteMaxChars)
			}
			lower := strings.ToLower(site)
			if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
				return fmt.Errorf("website %q must start with http:// or https://", site)
			}
		}
	}
	if update.Hours != nil {
		if err := validateBusinessHours(*update.Hours); err != nil {
			return err
		}
	}
	return nil
}

func validateBusinessHours(hours wa.BusinessHoursUpdate) error {
	if strings.TrimSpace(hours.TimeZone) == "" {
		return fmt.Errorf("--hours-timezone is required with --hours")
	}
	if _, err := time.LoadLocation(hours.TimeZone); err != nil || hours.TimeZone == "Local" {
		return fmt.Errorf("--hours-timezone %q is not an IANA time zone", hours.TimeZone)
	}
	seen := map[string]bool{}
	for _, day := range hours.Days {
		if !isBusinessDay(day.DayOfWeek) {
			return fmt.Errorf("day %q must be one of %s", day.DayOfWeek, strings.Join(businessDays, ", "))
		}
		if seen[day.DayOfWeek] {
			return fmt.Errorf("day %q is listed twice", day.DayOfWeek)
		}
		seen[day.DayOfWeek] = true
		switch day.Mode {
		case "open_24h", "appointment_only":
			if day.OpenTime != "" || day.CloseTime != "" {
				return fmt.Errorf("day %q: %s takes no open/close time", day.DayOfWeek, day.Mode)
			}
		case "specific_hours":
			open, err := parseMinutes(day.OpenTime)
			if err != nil {
				return fmt.Errorf("day %q: open time: %w", day.DayOfWeek, err)
			}
			closeAt, err := parseMinutes(day.CloseTime)
			if err != nil {
				return fmt.Errorf("day %q: close time: %w", day.DayOfWeek, err)
			}
			if open == closeAt {
				return fmt.Errorf("day %q: open and close times are equal", day.DayOfWeek)
			}
		default:
			return fmt.Errorf("day %q: mode %q must be specific_hours, open_24h or appointment_only", day.DayOfWeek, day.Mode)
		}
	}
	return nil
}

func isBusinessDay(day string) bool {
	for _, known := range businessDays {
		if day == known {
			return true
		}
	}
	return false
}

func parseMinutes(raw string) (int, error) {
	minutes, err := strconv.Atoi(raw)
	if err != nil || minutes < 0 || minutes > 1439 {
		return 0, fmt.Errorf("%q is not minutes from midnight (0-1439)", raw)
	}
	return minutes, nil
}

// parseHoursFlag turns `mon=09:00-18:00`, `sat=open_24h` or `sun=appointment_only`
// into a day entry with minutes from midnight.
func parseHoursFlag(raw string) (wa.BusinessHoursDay, error) {
	day, spec, ok := strings.Cut(strings.TrimSpace(raw), "=")
	day = strings.ToLower(strings.TrimSpace(day))
	spec = strings.TrimSpace(spec)
	if !ok || !isBusinessDay(day) || spec == "" {
		return wa.BusinessHoursDay{}, fmt.Errorf("--hours %q must look like mon=09:00-18:00, sat=open_24h or sun=appointment_only", raw)
	}
	switch spec {
	case "open_24h", "appointment_only":
		return wa.BusinessHoursDay{DayOfWeek: day, Mode: spec}, nil
	}
	openRaw, closeRaw, ok := strings.Cut(spec, "-")
	if !ok {
		return wa.BusinessHoursDay{}, fmt.Errorf("--hours %q must look like mon=09:00-18:00, sat=open_24h or sun=appointment_only", raw)
	}
	open, err := clockToMinutes(openRaw)
	if err != nil {
		return wa.BusinessHoursDay{}, fmt.Errorf("--hours %q: %w", raw, err)
	}
	closeAt, err := clockToMinutes(closeRaw)
	if err != nil {
		return wa.BusinessHoursDay{}, fmt.Errorf("--hours %q: %w", raw, err)
	}
	return wa.BusinessHoursDay{DayOfWeek: day, Mode: "specific_hours", OpenTime: strconv.Itoa(open), CloseTime: strconv.Itoa(closeAt)}, nil
}

func clockToMinutes(raw string) (int, error) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(raw), ":")
	if !ok || len(hh) != 2 || len(mm) != 2 {
		return 0, fmt.Errorf("time %q must be HH:MM", raw)
	}
	h, err1 := strconv.Atoi(hh)
	m, err2 := strconv.Atoi(mm)
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("time %q must be HH:MM", raw)
	}
	return h*60 + m, nil
}

func explainBusinessDelegateError(err error, kind, action string) error {
	if err == nil || !strings.Contains(err.Error(), "unsupported send kind") || !strings.Contains(err.Error(), kind) {
		return err
	}
	return fmt.Errorf("the running sync process does not support delegated %s; restart `wacli sync` after upgrading, then run this again: %w", action, err)
}

// runBusinessCommand runs one business IQ through the owner of the store lock
// when there is one, or over a connection of its own otherwise.
func runBusinessCommand(flags *rootFlags, req sendDelegateRequest, action string, execute func(context.Context, businessClient, sendDelegateRequest) (sendDelegateResponse, error)) (sendDelegateResponse, error) {
	if err := flags.requireWritable(); err != nil {
		return sendDelegateResponse{}, err
	}
	ctx, cancel := withTimeout(context.Background(), flags)
	defer cancel()

	a, lk, err := newApp(ctx, flags, true, false)
	if err != nil {
		resp, delegated, delegateErr := tryDelegateSend(ctx, flags, err, req)
		if delegated {
			if delegateErr != nil {
				return sendDelegateResponse{}, explainBusinessDelegateError(delegateErr, req.Kind, action)
			}
			return resp, nil
		}
		return sendDelegateResponse{}, err
	}
	defer closeApp(a, lk)
	if err := a.EnsureAuthed(ctx); err != nil {
		return sendDelegateResponse{}, err
	}
	if err := a.Connect(ctx, false, nil); err != nil {
		return sendDelegateResponse{}, err
	}
	return execute(ctx, a.WA(), req)
}

func newProfileBusinessGetCmd(flags *rootFlags) *cobra.Command {
	var targetRaw string
	cmd := &cobra.Command{
		Use:   "get [--jid <jid-or-phone>]",
		Short: "Fetch a full business profile (description, websites, hours); defaults to this account",
		Long: `Fetch a WhatsApp Business profile with every field WhatsApp Web shows:
description, address, email, websites, categories and weekly hours.

Without --jid it reads the linked account's own profile. When "wacli sync" owns
the store, the query runs through that process, so the line keeps one connection.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(targetRaw) != "" {
				if _, err := wa.ParseUserOrJID(targetRaw); err != nil {
					return err
				}
			}
			resp, err := runBusinessCommand(flags, sendDelegateRequest{Kind: businessProfileGetKind, JID: strings.TrimSpace(targetRaw)}, "business profile get", executeBusinessProfileGet)
			if err != nil {
				return err
			}
			if resp.BusinessProfile == nil {
				return fmt.Errorf("the running sync process did not return a business profile; restart `wacli sync` after upgrading")
			}
			return writeBusinessProfile(flags, resp.BusinessProfile)
		},
	}
	cmd.Flags().StringVar(&targetRaw, "jid", "", "target JID or phone number (default: this account)")
	return cmd
}

func newProfileBusinessSetCmd(flags *rootFlags) *cobra.Command {
	var description, address, email, timezone string
	var websites, hours []string
	var hoursClosed bool
	cmd := &cobra.Command{
		Use:   "set [--description ...] [--address ...] [--email ...] [--website ...] [--hours-timezone ... --hours day=spec ...]",
		Short: "Update this account's business profile (delta: unset flags leave fields unchanged)",
		Long: `Update the linked account's WhatsApp Business profile with the same delta
mutation WhatsApp Web sends. Only the flags you pass change; an empty value
clears that field. Limits: description 512, address 256, email 128 characters;
at most 2 websites of 256 characters starting with http:// or https://.

Hours replace the whole week: --hours mon=09:00-18:00 --hours sat=open_24h
--hours sun=appointment_only; days you do not list are closed. Pass
--hours-closed with --hours-timezone to close every day.

When "wacli sync" owns the store, the update runs through that process.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := businessProfileUpdateRequest{}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			if cmd.Flags().Changed("address") {
				req.Address = &address
			}
			if cmd.Flags().Changed("email") {
				req.Email = &email
			}
			if cmd.Flags().Changed("website") {
				req.WebsitesSet = true
				req.Websites = []string{}
				for _, site := range websites {
					site = strings.TrimSpace(site)
					if site == "" {
						continue
					}
					req.Websites = append(req.Websites, site)
				}
			}
			if len(hours) > 0 || hoursClosed || cmd.Flags().Changed("hours-timezone") {
				if len(hours) == 0 && !hoursClosed {
					return fmt.Errorf("--hours-timezone needs --hours day=spec entries or --hours-closed")
				}
				if len(hours) > 0 && hoursClosed {
					return fmt.Errorf("--hours-closed cannot be combined with --hours")
				}
				days := []wa.BusinessHoursDay{}
				for _, raw := range hours {
					day, err := parseHoursFlag(raw)
					if err != nil {
						return err
					}
					days = append(days, day)
				}
				req.Hours = &businessHoursUpdate{TimeZone: strings.TrimSpace(timezone), Days: days}
			}
			if err := validateBusinessProfileUpdate(req.toUpdate()); err != nil {
				return err
			}
			resp, err := runBusinessCommand(flags, sendDelegateRequest{Kind: businessProfileSetKind, BusinessProfile: &req}, "business profile set", executeBusinessProfileSet)
			if err != nil {
				return err
			}
			if resp.BusinessProfileSet == nil {
				return fmt.Errorf("the running sync process did not report the update; restart `wacli sync` after upgrading")
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, resp.BusinessProfileSet)
			}
			fmt.Fprintln(os.Stdout, "Business profile updated.")
			if resp.BusinessProfileSet.Profile != nil {
				return writeBusinessProfile(flags, resp.BusinessProfileSet.Profile)
			}
			if resp.BusinessProfileSet.ProfileError != "" {
				fmt.Fprintf(os.Stdout, "Could not re-read the profile: %s\n", sanitize(resp.BusinessProfileSet.ProfileError))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "business description (max 512 characters; \"\" clears)")
	cmd.Flags().StringVar(&address, "address", "", "business address (max 256 characters; \"\" clears)")
	cmd.Flags().StringVar(&email, "email", "", "contact email (max 128 characters; \"\" clears)")
	cmd.Flags().StringArrayVar(&websites, "website", nil, "website URL (repeatable, max 2; replaces the list; a single \"\" removes all)")
	cmd.Flags().StringVar(&timezone, "hours-timezone", "", "IANA time zone of the business hours (e.g. America/Lima)")
	cmd.Flags().StringArrayVar(&hours, "hours", nil, "weekly hours entry: day=HH:MM-HH:MM | day=open_24h | day=appointment_only (repeatable; unlisted days are closed)")
	cmd.Flags().BoolVar(&hoursClosed, "hours-closed", false, "close every day of the week (requires --hours-timezone)")
	return cmd
}

func writeBusinessProfile(flags *rootFlags, profile *wa.BusinessProfileFull) error {
	if flags.asJSON {
		return out.WriteJSON(os.Stdout, profile)
	}
	fmt.Fprintf(os.Stdout, "JID: %s\nDescription: %s\nAddress: %s\nEmail: %s\nWebsites: %s\nTimezone: %s\n",
		sanitize(profile.JID), sanitize(profile.Description), sanitize(profile.Address), sanitize(profile.Email),
		sanitize(strings.Join(profile.Websites, ", ")), sanitize(profile.BusinessHoursTimeZone))
	for _, category := range profile.Categories {
		fmt.Fprintf(os.Stdout, "Category: %s (%s)\n", sanitize(category.Name), sanitize(category.ID))
	}
	for _, day := range profile.BusinessHours {
		if day.Mode == "specific_hours" {
			fmt.Fprintf(os.Stdout, "Hours %s: %s-%s\n", day.DayOfWeek, minutesToClock(day.OpenTime), minutesToClock(day.CloseTime))
		} else {
			fmt.Fprintf(os.Stdout, "Hours %s: %s\n", day.DayOfWeek, day.Mode)
		}
	}
	return nil
}

func minutesToClock(raw string) string {
	minutes, err := parseMinutes(raw)
	if err != nil {
		return raw
	}
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}
