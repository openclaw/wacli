package resolve

import (
	"strings"
	"testing"

	"github.com/steipete/wacli/internal/store"
)

type fakeSource struct {
	contacts []store.Contact
	groups   []store.Group
	chats    []store.Chat
}

func (f *fakeSource) SearchContacts(query string, limit int) ([]store.Contact, error) {
	q := strings.ToLower(query)
	var out []store.Contact
	for _, c := range f.contacts {
		if contains(c.Name, q) || contains(c.Alias, q) || contains(c.Phone, q) || contains(c.JID, q) {
			out = append(out, c)
		}
	}
	return capN(out, limit), nil
}

func (f *fakeSource) ListGroups(query string, limit int) ([]store.Group, error) {
	q := strings.ToLower(query)
	var out []store.Group
	for _, g := range f.groups {
		if contains(g.Name, q) || contains(g.JID, q) {
			out = append(out, g)
		}
	}
	return capN(out, limit), nil
}

func (f *fakeSource) ListChats(query string, limit int) ([]store.Chat, error) {
	q := strings.ToLower(query)
	var out []store.Chat
	for _, c := range f.chats {
		if contains(c.Name, q) || contains(c.JID, q) {
			out = append(out, c)
		}
	}
	return capN(out, limit), nil
}

func contains(h, needle string) bool {
	return needle == "" || strings.Contains(strings.ToLower(h), needle)
}

func capN[T any](xs []T, n int) []T {
	if n > 0 && len(xs) > n {
		return xs[:n]
	}
	return xs
}

func TestLooksLikePhoneOrJID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"1234567890@s.whatsapp.net", true},
		{"12345@g.us", true},
		{"491701234567", true},
		{"+49 170 1234567", true},
		{"(415) 555-1212", true},
		{"", false},
		{"john", false},
		{"John Smith", false},
		{"mom", false},
		{"jose-maria", false},
		{"12a34", false},
	}
	for _, tc := range cases {
		if got := LooksLikePhoneOrJID(tc.in); got != tc.want {
			t.Errorf("LooksLikePhoneOrJID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"+49 170 1234567": "491701234567",
		"(415) 555-1212":  "4155551212",
		"491701234567":    "491701234567",
		"  ":              "",
	}
	for in, want := range cases {
		if got := NormalizePhone(in); got != want {
			t.Errorf("NormalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveExactMatchBeatsSubstring(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "Johnny Appleseed", Phone: "11111"},
			{JID: "2@s.whatsapp.net", Name: "John", Phone: "22222"},
			{JID: "3@s.whatsapp.net", Name: "Mary Johnson", Phone: "33333"},
		},
	}
	got, err := Resolve(src, "John", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) == 0 || got[0].JID != "2@s.whatsapp.net" {
		t.Fatalf("expected exact match 'John' first, got %+v", got)
	}
}

func TestResolveAliasedContactExactOnRealName(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "John Smith", Alias: "boss"},
			{JID: "2@s.whatsapp.net", Name: "Johnny Different"},
		},
	}
	got, err := Resolve(src, "John Smith", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) == 0 || got[0].JID != "1@s.whatsapp.net" || got[0].Score != ScoreExact {
		t.Fatalf("expected ScoreExact on real-name match, got %+v", got)
	}
}

func TestResolvePrefersAlias(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "Mother Dearest"},
			{JID: "2@s.whatsapp.net", Name: "Someone Else", Alias: "mom"},
		},
	}
	got, err := Resolve(src, "mom", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) == 0 || got[0].JID != "2@s.whatsapp.net" {
		t.Fatalf("expected alias 'mom' to win, got %+v", got)
	}
}

func TestResolveMatchesGroupsAndChats(t *testing.T) {
	src := &fakeSource{
		groups: []store.Group{
			{JID: "100@g.us", Name: "Family"},
		},
		chats: []store.Chat{
			{JID: "100@g.us", Name: "Family", Kind: "group"},
			{JID: "200@s.whatsapp.net", Name: "Family Lawyer", Kind: "dm"},
		},
	}
	got, err := Resolve(src, "family", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 || got[0].Name != "Family" {
		t.Fatalf("expected Family first and 2 unique JIDs, got %+v", got)
	}
}

func TestResolveSubstringHitScoresBelowExact(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "Trip 2024"},
		},
	}
	got, err := Resolve(src, "2024", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Score >= ScoreExact {
		t.Fatalf("expected substring hit below ScoreExact, got %+v", got)
	}
}

func TestResolveNumericName(t *testing.T) {
	src := &fakeSource{
		groups: []store.Group{
			{JID: "2024@g.us", Name: "2024"},
		},
	}
	got, err := Resolve(src, "2024", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].JID != "2024@g.us" {
		t.Fatalf("expected group '2024' to resolve, got %+v", got)
	}
}

func TestResolveKeepsHiddenFieldHits(t *testing.T) {
	src := &forcingSource{forced: []store.Contact{
		{JID: "push@s.whatsapp.net", Name: "Unrelated Display"},
	}}
	got, err := Resolve(src, "qwerty", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].JID != "push@s.whatsapp.net" || got[0].Score == 0 {
		t.Fatalf("expected hidden-field hit to survive with non-zero score, got %+v", got)
	}
}

type forcingSource struct {
	forced []store.Contact
}

func (f *forcingSource) SearchContacts(string, int) ([]store.Contact, error) {
	return f.forced, nil
}
func (f *forcingSource) ListGroups(string, int) ([]store.Group, error) { return nil, nil }
func (f *forcingSource) ListChats(string, int) ([]store.Chat, error)   { return nil, nil }

func TestResolveDropsJIDOnlyMatches(t *testing.T) {
	src := &fakeSource{
		groups: []store.Group{
			{JID: "123@g.us", Name: "Family"},
			{JID: "456@g.us", Name: "Poker Night"},
		},
	}
	got, err := Resolve(src, "us", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected zero hits for JID-only query, got %+v", got)
	}
}

func TestResolveEmptyInputErrors(t *testing.T) {
	if _, err := Resolve(&fakeSource{}, "   ", 10); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestResolveWordBoundary(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "John Smith"},
			{JID: "2@s.whatsapp.net", Name: "Smithson Holdings"},
		},
	}
	got, err := Resolve(src, "smith", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) < 2 || got[0].Name != "Smithson Holdings" {
		t.Fatalf("expected Smithson Holdings first, got %+v", got)
	}
}
