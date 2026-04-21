package main

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestPresenceMediaFromString(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    types.ChatPresenceMedia
		wantErr bool
	}{
		{name: "empty", input: "", want: ""},
		{name: "audio", input: "audio", want: types.ChatPresenceMediaAudio},
		{name: "trimmed case", input: " Audio ", want: types.ChatPresenceMediaAudio},
		{name: "unknown", input: "video", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := presenceMediaFromString(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("presenceMediaFromString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
