package main

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestValidateLocationOptions(t *testing.T) {
	tests := []struct {
		name      string
		lat       float64
		lng       float64
		place     string
		latSet    bool
		lngSet    bool
		wantError string
		wantName  string
	}{
		{name: "valid", lat: 51.4779, lng: -0.0015, place: "Head office", latSet: true, lngSet: true, wantName: "Head office"},
		{name: "trims name", lat: 0, lng: 0, place: "  Null Island  ", latSet: true, lngSet: true, wantName: "Null Island"},
		{name: "explicit zero is a real coordinate", lat: 0, lng: 0, latSet: true, lngSet: true},
		{name: "bounds are inclusive", lat: -90, lng: 180, latSet: true, lngSet: true},
		{name: "missing latitude", lng: -0.0015, lngSet: true, wantError: "--latitude and --longitude are required"},
		{name: "missing longitude", lat: 51.4779, latSet: true, wantError: "--latitude and --longitude are required"},
		{name: "latitude too low", lat: -90.1, lng: 0, latSet: true, lngSet: true, wantError: "--latitude must be between"},
		{name: "latitude too high", lat: 90.1, lng: 0, latSet: true, lngSet: true, wantError: "--latitude must be between"},
		{name: "longitude too low", lat: 0, lng: -180.1, latSet: true, lngSet: true, wantError: "--longitude must be between"},
		{name: "longitude too high", lat: 0, lng: 180.1, latSet: true, lngSet: true, wantError: "--longitude must be between"},
		{name: "NaN latitude", lat: math.NaN(), lng: 0, latSet: true, lngSet: true, wantError: "must be finite"},
		{name: "infinite longitude", lat: 0, lng: math.Inf(1), latSet: true, lngSet: true, wantError: "must be finite"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := validateLocationOptions(tc.lat, tc.lng, tc.place, tc.latSet, tc.lngSet)
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantError)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error containing %q, got %q", tc.wantError, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opts.Latitude != tc.lat || opts.Longitude != tc.lng {
				t.Fatalf("coordinates = %v,%v want %v,%v", opts.Latitude, opts.Longitude, tc.lat, tc.lng)
			}
			if opts.Name != tc.wantName {
				t.Fatalf("name = %q want %q", opts.Name, tc.wantName)
			}
		})
	}
}

func TestBuildLocationMessage(t *testing.T) {
	msg := buildLocationMessage(locationOptions{Latitude: 51.4779, Longitude: -0.0015, Name: "Head office"})

	loc := msg.GetLocationMessage()
	if loc == nil {
		t.Fatal("expected a LocationMessage")
	}
	if got := loc.GetDegreesLatitude(); got != 51.4779 {
		t.Fatalf("latitude = %v want 51.4779", got)
	}
	if got := loc.GetDegreesLongitude(); got != -0.0015 {
		t.Fatalf("longitude = %v want -0.0015", got)
	}
	if got := loc.GetName(); got != "Head office" {
		t.Fatalf("name = %q want %q", got, "Head office")
	}
}

func TestBuildLocationMessageOmitsEmptyName(t *testing.T) {
	msg := buildLocationMessage(locationOptions{Latitude: 1, Longitude: 2, Name: "   "})

	loc := msg.GetLocationMessage()
	if loc == nil {
		t.Fatal("expected a LocationMessage")
	}
	if loc.Name != nil {
		t.Fatalf("expected an unset name, got %q", loc.GetName())
	}
}

func TestSendLocationMessageSendsProtoToRecipient(t *testing.T) {
	sender := &recordingTextSender{nextProtoMsgID: "loc-id"}
	to := types.JID{User: "15551234567", Server: types.DefaultUserServer}

	id, err := sendLocationMessage(context.Background(), sender, to, locationOptions{
		Latitude:  51.4779,
		Longitude: -0.0015,
		Name:      "Head office",
	})
	if err != nil {
		t.Fatalf("sendLocationMessage: %v", err)
	}
	if id != "loc-id" {
		t.Fatalf("message id = %q want %q", id, "loc-id")
	}
	if sender.protoCalls != 1 {
		t.Fatalf("expected exactly one proto send, got %d", sender.protoCalls)
	}
	if sender.textCalls != 0 {
		t.Fatalf("expected no plain text send, got %d", sender.textCalls)
	}
	if sender.protoRecipient != to {
		t.Fatalf("recipient = %v want %v", sender.protoRecipient, to)
	}
	if got := sender.protoMsg.GetLocationMessage().GetDegreesLatitude(); got != 51.4779 {
		t.Fatalf("wire latitude = %v want 51.4779", got)
	}
}

func TestPersistOutboundLocationCanonicalizesChatAndSetsDisplayText(t *testing.T) {
	db := openSendTestDB(t)
	lid := types.JID{User: "999123456789", Server: types.HiddenUserServer}
	pn := types.JID{User: "15551234567", Server: types.DefaultUserServer}
	resolver := outboundTextResolverStub{lid: lid, pn: pn}

	persistOutboundLocationWith(context.Background(), db, resolver, lid, "loc-1", locationOptions{Latitude: 51.4779, Longitude: -0.0015, Name: "Head office"}, time.Now().UTC())

	msg, err := db.GetMessage(pn.String(), "loc-1")
	if err != nil {
		t.Fatalf("GetMessage under the phone JID: %v", err)
	}
	if !msg.FromMe {
		t.Fatal("expected the stored location to be outbound")
	}
	if msg.DisplayText != locationDisplayText {
		t.Fatalf("display_text = %q want %q", msg.DisplayText, locationDisplayText)
	}
	if strings.TrimSpace(msg.Text) != "" {
		t.Fatalf("expected no body text on a location, got %q", msg.Text)
	}
}

func TestExecuteDelegatedSendRejectsOutOfRangeLocation(t *testing.T) {
	_, err := executeDelegatedSend(context.Background(), nil, sendDelegateRequest{
		Version:   sendDelegateVersion,
		Kind:      "location",
		To:        "15551234567",
		Latitude:  120,
		Longitude: 0,
	})
	if err == nil {
		t.Fatal("expected out-of-range latitude to be rejected before any send")
	}
	if !strings.Contains(err.Error(), "--latitude must be between") {
		t.Fatalf("expected a latitude bounds error, got %q", err.Error())
	}
}
