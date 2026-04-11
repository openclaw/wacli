package main

import "testing"

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"+15551234567", "15551234567"},
		{"15551234567", "15551234567"},
		{"+44 7451 294587", "44 7451 294587"},   // spaces preserved — WA API strips them
		{"  +49123456789  ", "49123456789"},
		{"+1 (555) 123-4567", "1 (555) 123-4567"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := normalizePhone(tc.input)
			if got != tc.want {
				t.Errorf("normalizePhone(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
