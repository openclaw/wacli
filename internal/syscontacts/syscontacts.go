package syscontacts

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"unicode"
)

type Contact struct {
	FirstName string   `json:"first_name"`
	LastName  string   `json:"last_name"`
	FullName  string   `json:"full_name"`
	Phones    []string `json:"phones"`
}

func (c Contact) Name() string {
	if strings.TrimSpace(c.FullName) != "" {
		return strings.TrimSpace(c.FullName)
	}
	return strings.TrimSpace(strings.Join([]string{c.FirstName, c.LastName}, " "))
}

func Decode(r io.Reader) ([]Contact, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var contacts []Contact
		if err := json.Unmarshal([]byte(trimmed), &contacts); err != nil {
			return nil, err
		}
		return contacts, nil
	}

	var contacts []Contact
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c Contact
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, scanner.Err()
}

func PhoneToName(contacts []Contact) map[string]string {
	out := map[string]string{}
	for _, c := range contacts {
		name := c.Name()
		if name == "" {
			continue
		}
		for _, phone := range c.Phones {
			normalized := NormalizePhone(phone)
			if len(normalized) < 7 {
				continue
			}
			if _, exists := out[normalized]; !exists {
				out[normalized] = name
			}
		}
	}
	return out
}

func NormalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if strings.HasPrefix(out, "00") {
		out = strings.TrimPrefix(out, "00")
	}
	return out
}
