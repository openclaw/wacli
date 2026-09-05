package syscontacts

import (
	"io"
	"strings"
	"testing"
)

func TestNormalizePhone(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"+1 (415) 734-7847", "14157347847"},
		{"0043 664 104 2436", "436641042436"},
		{"14157347847", "14157347847"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizePhone(tt.in); got != tt.want {
			t.Fatalf("NormalizePhone(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDecodeSupportsJSONArrayAndNDJSON(t *testing.T) {
	for _, input := range []string{
		`[{"full_name":"Alice","phones":["+1 (415) 734-7847"]}]`,
		"{\"full_name\":\"Alice\",\"phones\":[\"+1 (415) 734-7847\"]}\n",
	} {
		contacts, err := Decode(strings.NewReader(input))
		if err != nil {
			t.Fatalf("Decode(%q): %v", input, err)
		}
		if len(contacts) != 1 || contacts[0].Name() != "Alice" {
			t.Fatalf("contacts = %#v", contacts)
		}
	}
}

func TestDecodeRejectsOversizeStream(t *testing.T) {
	r := io.LimitReader(repeatReader{ch: 'x'}, int64(MaxContactsDecodeBytes)+1)
	_, err := Decode(r)
	if err == nil {
		t.Fatal("Decode(oversize stream): want error, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("Decode(oversize stream): %v", err)
	}
}

func TestDecodeAcceptsFullImportBudget(t *testing.T) {
	const contact = `[{"full_name":"Alice","phones":["+15551234567"]}]`
	input := contact + strings.Repeat(" ", MaxContactsDecodeBytes-len(contact))
	contacts, err := Decode(strings.NewReader(input))
	if err != nil || len(contacts) != 1 || contacts[0].Name() != "Alice" {
		t.Fatalf("Decode at 10 MiB boundary: contacts=%v err=%v", contacts, err)
	}
}

type repeatReader struct{ ch byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.ch
	}
	return len(p), nil
}

func TestPhoneToNameKeepsFirstNameForDuplicateNumber(t *testing.T) {
	got := PhoneToName([]Contact{
		{FullName: "Alice", Phones: []string{"+1 (415) 734-7847"}},
		{FullName: "Other", Phones: []string{"14157347847"}},
	})
	if got["14157347847"] != "Alice" {
		t.Fatalf("phone map = %#v", got)
	}
}
