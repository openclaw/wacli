package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

type businessFake struct {
	linked    string
	gotJID    types.JID
	gotUpdate *wa.BusinessProfileUpdate
	getCalls  int
	getErr    error
	updateErr error
}

func (f *businessFake) LinkedJID() string { return f.linked }
func (f *businessFake) GetBusinessProfileFull(_ context.Context, jid types.JID) (*wa.BusinessProfileFull, error) {
	f.gotJID = jid
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &wa.BusinessProfileFull{JID: jid.String(), Description: "desc", Websites: []string{"https://lenz.pe"}}, nil
}
func (f *businessFake) UpdateBusinessProfile(_ context.Context, update wa.BusinessProfileUpdate) error {
	f.gotUpdate = &update
	return f.updateErr
}

func TestBusinessProfileGetDefaultsToLinkedAccount(t *testing.T) {
	f := &businessFake{linked: "51905448483:7@s.whatsapp.net"}
	resp, err := executeBusinessProfileGet(context.Background(), f, sendDelegateRequest{Kind: businessProfileGetKind})
	if err != nil || !resp.OK || resp.BusinessProfile == nil {
		t.Fatalf("%+v %v", resp, err)
	}
	if f.gotJID.String() != "51905448483@s.whatsapp.net" || resp.BusinessProfile.Description != "desc" {
		t.Fatalf("jid %s profile %+v", f.gotJID, resp.BusinessProfile)
	}
	// Explicit target, phone form, is normalized.
	if _, err := executeBusinessProfileGet(context.Background(), f, sendDelegateRequest{JID: "+51 964 694 693"}); err != nil {
		t.Fatal(err)
	}
	if f.gotJID.String() != "51964694693@s.whatsapp.net" {
		t.Fatal(f.gotJID)
	}
	if _, err := executeBusinessProfileGet(context.Background(), &businessFake{}, sendDelegateRequest{}); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatal(err)
	}
	f.getErr = errors.New("iq timeout")
	if _, err := executeBusinessProfileGet(context.Background(), f, sendDelegateRequest{}); err == nil || !strings.Contains(err.Error(), "get business profile: iq timeout") {
		t.Fatal(err)
	}
}

func TestBusinessProfileSetValidatesSendsAndRereads(t *testing.T) {
	f := &businessFake{linked: "51905448483@s.whatsapp.net"}
	desc := "Hola"
	req := sendDelegateRequest{Kind: businessProfileSetKind, BusinessProfile: &businessProfileUpdateRequest{
		Description: &desc, WebsitesSet: true, Websites: []string{"https://lenz.pe"},
		Hours: &businessHoursUpdate{TimeZone: "America/Lima", Days: []wa.BusinessHoursDay{{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"}}},
	}}
	resp, err := executeBusinessProfileSet(context.Background(), f, req)
	if err != nil || !resp.OK || resp.BusinessProfileSet == nil || !resp.BusinessProfileSet.Updated {
		t.Fatalf("%+v %v", resp, err)
	}
	if f.gotUpdate == nil || *f.gotUpdate.Description != "Hola" || len(f.gotUpdate.Websites) != 1 || f.gotUpdate.Hours == nil || f.gotUpdate.Address != nil {
		t.Fatalf("%+v", f.gotUpdate)
	}
	if resp.BusinessProfileSet.Profile == nil || resp.BusinessProfileSet.Profile.JID != "51905448483@s.whatsapp.net" || f.getCalls != 1 {
		t.Fatalf("%+v calls=%d", resp.BusinessProfileSet, f.getCalls)
	}

	// A failed re-read is reported, not treated as a failed update.
	f = &businessFake{linked: "51905448483@s.whatsapp.net", getErr: errors.New("slow")}
	resp, err = executeBusinessProfileSet(context.Background(), f, req)
	if err != nil || !resp.BusinessProfileSet.Updated || resp.BusinessProfileSet.Profile != nil || !strings.Contains(resp.BusinessProfileSet.ProfileError, "slow") {
		t.Fatalf("%+v %v", resp.BusinessProfileSet, err)
	}

	// Clearing every website sends an empty, non-nil list.
	f = &businessFake{linked: "1@s.whatsapp.net"}
	if _, err := executeBusinessProfileSet(context.Background(), f, sendDelegateRequest{BusinessProfile: &businessProfileUpdateRequest{WebsitesSet: true}}); err != nil {
		t.Fatal(err)
	}
	if f.gotUpdate.Websites == nil || len(f.gotUpdate.Websites) != 0 {
		t.Fatalf("%#v", f.gotUpdate.Websites)
	}

	// Invalid input never reaches the wire; a wire failure is wrapped.
	long := strings.Repeat("x", businessDescriptionMaxChars+1)
	f = &businessFake{linked: "1@s.whatsapp.net"}
	if _, err := executeBusinessProfileSet(context.Background(), f, sendDelegateRequest{BusinessProfile: &businessProfileUpdateRequest{Description: &long}}); err == nil || f.gotUpdate != nil {
		t.Fatalf("%v %+v", err, f.gotUpdate)
	}
	if _, err := executeBusinessProfileSet(context.Background(), f, sendDelegateRequest{}); err == nil {
		t.Fatal("nil update accepted")
	}
	f.updateErr = errors.New("server refused")
	if _, err := executeBusinessProfileSet(context.Background(), f, sendDelegateRequest{BusinessProfile: &businessProfileUpdateRequest{Description: &desc}}); err == nil || !strings.Contains(err.Error(), "set business profile: server refused") {
		t.Fatal(err)
	}
}

func TestValidateBusinessProfileUpdate(t *testing.T) {
	str := func(s string) *string { return &s }
	hours := func(days ...wa.BusinessHoursDay) *wa.BusinessHoursUpdate {
		return &wa.BusinessHoursUpdate{TimeZone: "America/Lima", Days: days}
	}
	cases := []struct {
		name   string
		update wa.BusinessProfileUpdate
		want   string // "" = valid
	}{
		{"empty", wa.BusinessProfileUpdate{}, "nothing to update"},
		{"clear description", wa.BusinessProfileUpdate{Description: str("")}, ""},
		{"max description", wa.BusinessProfileUpdate{Description: str(strings.Repeat("é", 512))}, ""},
		{"long description", wa.BusinessProfileUpdate{Description: str(strings.Repeat("é", 513))}, "description exceeds 512"},
		{"long address", wa.BusinessProfileUpdate{Address: str(strings.Repeat("a", 257))}, "address exceeds 256"},
		{"email ok", wa.BusinessProfileUpdate{Email: str("ventas@lenz.pe")}, ""},
		{"email clear", wa.BusinessProfileUpdate{Email: str("")}, ""},
		{"email bad", wa.BusinessProfileUpdate{Email: str("ventas")}, "not a plain address"},
		{"email with name", wa.BusinessProfileUpdate{Email: str("LENZ <ventas@lenz.pe>")}, "not a plain address"},
		{"email long", wa.BusinessProfileUpdate{Email: str(strings.Repeat("a", 125) + "@b.pe")}, "email exceeds 128"},
		{"websites ok", wa.BusinessProfileUpdate{Websites: []string{"https://lenz.pe", "HTTP://x.pe"}}, ""},
		{"websites none", wa.BusinessProfileUpdate{Websites: []string{}}, ""},
		{"websites three", wa.BusinessProfileUpdate{Websites: []string{"https://a", "https://b", "https://c"}}, "at most 2 websites"},
		{"website scheme", wa.BusinessProfileUpdate{Websites: []string{"lenz.pe"}}, "must start with http"},
		{"website padded", wa.BusinessProfileUpdate{Websites: []string{" https://lenz.pe"}}, "empty or padded"},
		{"website long", wa.BusinessProfileUpdate{Websites: []string{"https://" + strings.Repeat("a", 250)}}, "website exceeds 256"},
		{"hours ok", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"}, wa.BusinessHoursDay{DayOfWeek: "sun", Mode: "open_24h"})}, ""},
		{"hours closed week", wa.BusinessProfileUpdate{Hours: hours()}, ""},
		{"hours no tz", wa.BusinessProfileUpdate{Hours: &wa.BusinessHoursUpdate{Days: []wa.BusinessHoursDay{}}}, "--hours-timezone is required"},
		{"hours bad tz", wa.BusinessProfileUpdate{Hours: &wa.BusinessHoursUpdate{TimeZone: "Lima/Peru"}}, "not an IANA time zone"},
		{"hours bad day", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "monday", Mode: "open_24h"})}, `day "monday" must be one of`},
		{"hours dup day", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "open_24h"}, wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "open_24h"})}, "listed twice"},
		{"hours bad mode", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "closed"})}, `mode "closed"`},
		{"hours 24h with time", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "open_24h", OpenTime: "1"})}, "takes no open/close time"},
		{"hours bad minutes", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "09:00", CloseTime: "1080"})}, "open time"},
		{"hours over range", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "0", CloseTime: "1440"})}, "close time"},
		{"hours equal", wa.BusinessProfileUpdate{Hours: hours(wa.BusinessHoursDay{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "600", CloseTime: "600"})}, "are equal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBusinessProfileUpdate(tc.update)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseHoursFlag(t *testing.T) {
	good := map[string]wa.BusinessHoursDay{
		"mon=09:00-18:00":      {DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"},
		" SAT = open_24h ":     {DayOfWeek: "sat", Mode: "open_24h"},
		"sun=appointment_only": {DayOfWeek: "sun", Mode: "appointment_only"},
		"fri=00:00-23:59":      {DayOfWeek: "fri", Mode: "specific_hours", OpenTime: "0", CloseTime: "1439"},
	}
	for raw, want := range good {
		got, err := parseHoursFlag(raw)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v; want %+v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "mon", "mon=", "lun=09:00-18:00", "mon=9:00-18:00", "mon=09:00", "mon=24:00-25:00", "mon=09:60-10:00", "mon=closed"} {
		if _, err := parseHoursFlag(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if minutesToClock("540") != "09:00" || minutesToClock("x") != "x" {
		t.Fatal("minutesToClock")
	}
}

func TestBusinessReadsBypassSendQueueAndSetDoesNotHoldIt(t *testing.T) {
	for _, kind := range []string{businessProfileGetKind, businessProfileSetKind} {
		t.Run(kind, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(2 * time.Second))
			f := &businessFake{linked: "1@s.whatsapp.net"}
			desc := "x"
			// An empty send slot blocks every message send; business IQs must not wait for it.
			go handleSendDelegateConn(context.Background(), server, func(ctx context.Context, req sendDelegateRequest) (sendDelegateResponse, error) {
				switch req.Kind {
				case businessProfileGetKind:
					return executeBusinessProfileGet(ctx, f, req)
				default:
					return executeBusinessProfileSet(ctx, f, req)
				}
			}, make(chan struct{}, 1), newSendPacer(sendSpacing{}))
			req := sendDelegateRequest{Version: sendDelegateVersion, Kind: kind, TimeoutMS: 1000}
			if kind == businessProfileSetKind {
				req.BusinessProfile = &businessProfileUpdateRequest{Description: &desc}
			}
			if err := json.NewEncoder(client).Encode(req); err != nil {
				t.Fatal(err)
			}
			var resp sendDelegateResponse
			if err := json.NewDecoder(client).Decode(&resp); err != nil {
				t.Fatal(err)
			}
			if !resp.OK {
				t.Fatalf("%+v", resp)
			}
			switch kind {
			case businessProfileGetKind:
				if resp.BusinessProfile == nil {
					t.Fatalf("%+v", resp)
				}
			default:
				if resp.BusinessProfileSet == nil || !resp.BusinessProfileSet.Updated {
					t.Fatalf("%+v", resp)
				}
			}
		})
	}
}

func TestBusinessRequestRoundTripsThroughJSON(t *testing.T) {
	desc := ""
	in := sendDelegateRequest{Version: 1, Kind: businessProfileSetKind, BusinessProfile: &businessProfileUpdateRequest{
		Description: &desc, WebsitesSet: true, Websites: []string{},
		Hours: &businessHoursUpdate{TimeZone: "UTC", Days: []wa.BusinessHoursDay{}},
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out sendDelegateRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.BusinessProfile == nil || out.BusinessProfile.Description == nil || *out.BusinessProfile.Description != "" || !out.BusinessProfile.WebsitesSet {
		t.Fatalf("%s -> %+v", raw, out.BusinessProfile)
	}
	update := out.BusinessProfile.toUpdate()
	if update.Websites == nil || len(update.Websites) != 0 || update.Hours == nil || update.Hours.Days == nil || len(update.Hours.Days) != 0 {
		t.Fatalf("%+v", update)
	}
}

func TestOldDaemonRefusalIsExplained(t *testing.T) {
	err := explainBusinessDelegateError(errors.New(`unsupported send kind "business_profile_set"`), businessProfileSetKind, "business profile set")
	if err == nil || !strings.Contains(err.Error(), "restart `wacli sync` after upgrading") {
		t.Fatal(err)
	}
	other := errors.New("boom")
	if explainBusinessDelegateError(other, businessProfileSetKind, "business profile set") != other {
		t.Fatal("unrelated errors must pass through")
	}
	// The real dispatcher of an older build answers exactly that string for a new kind.
	if _, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{Version: sendDelegateVersion, Kind: "business_profile_future"}); err == nil || !strings.Contains(err.Error(), `unsupported send kind "business_profile_future"`) {
		t.Fatal(err)
	}
}

func TestProfileBusinessCommandsAreRegistered(t *testing.T) {
	var flags rootFlags
	business := newProfileBusinessCmd(&flags)
	names := map[string]bool{}
	for _, sub := range business.Commands() {
		names[sub.Name()] = true
	}
	if !names["get"] || !names["set"] || business.RunE == nil {
		t.Fatalf("business subcommands %v, legacy RunE kept: %v", names, business.RunE != nil)
	}
	catalog := newCatalogCmd(&flags)
	if len(catalog.Commands()) != 1 || catalog.Commands()[0].Name() != "list" {
		t.Fatalf("%v", catalog.Commands())
	}
	// The legacy form keeps working: `profile business --jid` still resolves to the parent.
	profile := newProfileCmd(&flags)
	cmd, args, err := profile.Find([]string{"business", "--jid", "1@s.whatsapp.net"})
	if err != nil || cmd.Name() != "business" || len(args) != 2 {
		t.Fatalf("%v %v %v", cmd, args, err)
	}
	cmd, _, err = profile.Find([]string{"business", "get", "--jid", "1@s.whatsapp.net"})
	if err != nil || cmd.Name() != "get" {
		t.Fatalf("%v %v", cmd, err)
	}
}
