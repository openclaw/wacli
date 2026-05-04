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

func TestLooksLikePhone(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"491701234567", true},
		{"+49 170 1234567", true},
		{"(415) 555-1212", true},
		{"123.456.7890", true},
		{"1234567890@s.whatsapp.net", false},
		{"john", false},
		{"jose-maria", false},
		{"12a34", false},
		{"++1234567890", false},
	} {
		if got := LooksLikePhone(tc.in); got != tc.want {
			t.Errorf("LooksLikePhone(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNormalizePhone(t *testing.T) {
	if got := NormalizePhone("+49 (170) 123-45.67"); got != "491701234567" {
		t.Fatalf("NormalizePhone = %q", got)
	}
}

func TestResolveRanksExactAndAliases(t *testing.T) {
	src := &fakeSource{
		contacts: []store.Contact{
			{JID: "1@s.whatsapp.net", Name: "Johnny Appleseed"},
			{JID: "2@s.whatsapp.net", Name: "John"},
			{JID: "3@s.whatsapp.net", Name: "Someone Else", Alias: "mom"},
		},
	}
	got, err := Resolve(src, "John", 10)
	if err != nil {
		t.Fatalf("Resolve John: %v", err)
	}
	if len(got) == 0 || got[0].JID != "2@s.whatsapp.net" {
		t.Fatalf("expected exact John first, got %+v", got)
	}

	got, err = Resolve(src, "mom", 10)
	if err != nil {
		t.Fatalf("Resolve mom: %v", err)
	}
	if len(got) != 1 || got[0].JID != "3@s.whatsapp.net" || got[0].Name != "mom" {
		t.Fatalf("expected alias mom, got %+v", got)
	}
}

func TestResolveMatchesGroupsAndChats(t *testing.T) {
	src := &fakeSource{
		groups: []store.Group{{JID: "100@g.us", Name: "Family"}},
		chats: []store.Chat{
			{JID: "100@g.us", Name: "Family", Kind: "group"},
			{JID: "200@s.whatsapp.net", Name: "Family Lawyer", Kind: "dm"},
		},
	}
	got, err := Resolve(src, "family", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 || got[0].JID != "100@g.us" || got[0].Kind != KindGroup {
		t.Fatalf("expected group and lawyer chat, got %+v", got)
	}
}

func TestResolveDropsJIDOnlyMatches(t *testing.T) {
	src := &fakeSource{
		groups: []store.Group{
			{JID: "123@g.us", Name: "Family"},
			{JID: "456@g.us", Name: "Poker Night"},
		},
		chats: []store.Chat{
			{JID: "789@g.us", Name: "Work", Kind: "group"},
		},
	}
	got, err := Resolve(src, "us", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no JID-only hits, got %+v", got)
	}
}

func TestResolveKeepsHiddenFieldHits(t *testing.T) {
	src := &forcingSource{forced: []store.Contact{
		{JID: "push@s.whatsapp.net", Name: "Visible Name"},
	}}
	got, err := Resolve(src, "hidden", 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 || got[0].Score == 0 {
		t.Fatalf("expected hidden-field hit to survive, got %+v", got)
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
