package resolve

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/steipete/wacli/internal/store"
)

type Kind string

const (
	KindContact Kind = "contact"
	KindGroup   Kind = "group"
	KindChat    Kind = "chat"
)

const (
	ScoreExact     = 100
	scorePrefix    = 60
	scoreWordStart = 30
	scoreSubstring = 10
)

type Candidate struct {
	JID    string
	Name   string
	Detail string
	Kind   Kind
	Score  int
}

type Source interface {
	SearchContacts(query string, limit int) ([]store.Contact, error)
	ListGroups(query string, limit int) ([]store.Group, error)
	ListChats(query string, limit int) ([]store.Chat, error)
}

func LooksLikePhoneOrJID(input string) bool {
	s := strings.TrimSpace(input)
	if s == "" {
		return false
	}
	if strings.Contains(s, "@") {
		return true
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == '+' || r == '-' || r == ' ' || r == '(' || r == ')':
		default:
			return false
		}
	}
	return hasDigit
}

func NormalizePhone(input string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(input) {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func Resolve(src Source, input string, limit int) ([]Candidate, error) {
	q := strings.TrimSpace(input)
	if q == "" {
		return nil, fmt.Errorf("recipient is required")
	}
	if limit <= 0 {
		limit = 10
	}

	const perSource = math.MaxInt32

	seen := make(map[string]int)
	var out []Candidate

	add := func(c Candidate) {
		if c.JID == "" {
			return
		}
		if idx, ok := seen[c.JID]; ok {
			if c.Score > out[idx].Score || (c.Score == out[idx].Score && c.Kind == KindContact) {
				out[idx] = c
			}
			return
		}
		seen[c.JID] = len(out)
		out = append(out, c)
	}

	qLower := strings.ToLower(q)

	contacts, err := src.SearchContacts(q, perSource)
	if err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	for _, c := range contacts {
		display := pickContactName(c)
		if display == "" && c.Phone == "" {
			continue
		}
		score := rank(q, c.Name, c.Alias, c.Phone)
		if score == 0 && !strings.Contains(strings.ToLower(c.JID), qLower) {
			score = scoreSubstring
		}
		if score == 0 {
			continue
		}
		add(Candidate{
			JID:    c.JID,
			Name:   display,
			Detail: c.Phone,
			Kind:   KindContact,
			Score:  score,
		})
	}

	groups, err := src.ListGroups(q, perSource)
	if err != nil {
		return nil, fmt.Errorf("search groups: %w", err)
	}
	for _, g := range groups {
		if strings.TrimSpace(g.Name) == "" {
			continue
		}
		score := rank(q, g.Name)
		if score == 0 {
			continue
		}
		add(Candidate{
			JID:    g.JID,
			Name:   g.Name,
			Detail: "group",
			Kind:   KindGroup,
			Score:  score,
		})
	}

	chats, err := src.ListChats(q, perSource)
	if err != nil {
		return nil, fmt.Errorf("search chats: %w", err)
	}
	for _, ch := range chats {
		if strings.TrimSpace(ch.Name) == "" {
			continue
		}
		score := rank(q, ch.Name)
		if score == 0 {
			continue
		}
		add(Candidate{
			JID:    ch.JID,
			Name:   ch.Name,
			Detail: ch.Kind,
			Kind:   KindChat,
			Score:  score,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func pickContactName(c store.Contact) string {
	if s := strings.TrimSpace(c.Alias); s != "" {
		return s
	}
	if s := strings.TrimSpace(c.Name); s != "" {
		return s
	}
	return strings.TrimSpace(c.Phone)
}

func rank(query string, haystacks ...string) int {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0
	}
	best := 0
	for _, h := range haystacks {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		score := 0
		switch {
		case h == q:
			score = ScoreExact
		case strings.HasPrefix(h, q):
			score = scorePrefix
		case hasWordPrefix(h, q):
			score = scoreWordStart
		case strings.Contains(h, q):
			score = scoreSubstring
		}
		if score > best {
			best = score
		}
	}
	return best
}

func hasWordPrefix(h, q string) bool {
	for _, word := range strings.Fields(h) {
		if strings.HasPrefix(word, q) {
			return true
		}
	}
	return false
}
