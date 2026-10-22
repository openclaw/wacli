package wa

import (
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

// iPhone contact cards group each number with its label ("item1.TEL" +
// "item1.X-ABLabel"); the grouped TEL used to be ignored, losing the number.
func TestContactDisplayTextReadsGroupedVCardProperties(t *testing.T) {
	vcard := "BEGIN:VCARD\r\n" +
		"VERSION:3.0\r\n" +
		"N:Rossi;Mario;;;\r\n" +
		"item1.FN:Mario Rossi\r\n" +
		"item1.TEL;type=CELL;type=VOICE;waid=393331234567:+39 333 123 4567\r\n" +
		"item1.X-ABLabel:Mobile\r\n" +
		"item2.TEL;type=WORK:+39 02 1234567\r\n" +
		"item2.X-ABLabel:Club\r\n" +
		"END:VCARD"

	got := contactDisplayText(&waProto.ContactMessage{Vcard: proto.String(vcard)})
	want := "Contact: Mario Rossi (+39 333 123 4567, +39 02 1234567)"
	if got != want {
		t.Fatalf("contactDisplayText = %q, want %q", got, want)
	}
}

func TestContactDisplayTextKeepsDisplayNameOverGroupedFN(t *testing.T) {
	got := contactDisplayText(&waProto.ContactMessage{
		DisplayName: proto.String("Referente Club"),
		Vcard:       proto.String("BEGIN:VCARD\nitem1.FN:Mario Rossi\nitem1.TEL:+39 333 1234567\nEND:VCARD"),
	})
	if got != "Contact: Referente Club (+39 333 1234567)" {
		t.Fatalf("contactDisplayText = %q", got)
	}
}

func TestVCardPropertyName(t *testing.T) {
	cases := map[string]string{
		"TEL":                         "TEL",
		"tel;type=CELL":               "TEL",
		"item1.TEL;type=CELL;waid=39": "TEL",
		"item12.fn":                   "FN",
		" item1.TEL ":                 "TEL",
		"X-ABLabel":                   "X-ABLABEL",
	}
	for key, want := range cases {
		if got := vCardPropertyName(key); got != want {
			t.Errorf("vCardPropertyName(%q) = %q, want %q", key, got, want)
		}
	}
}
