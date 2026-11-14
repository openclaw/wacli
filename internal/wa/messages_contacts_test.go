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

func TestExtractContactKeepsRawVCardAndTelParameters(t *testing.T) {
	vcard := "BEGIN:VCARD\nVERSION:3.0\nFN:Ana Prueba\nTEL;type=CELL;waid=51900000001:+51 900 000 001\nEND:VCARD"
	var pm ParsedMessage
	extractContactText(&waProto.Message{ContactMessage: &waProto.ContactMessage{
		DisplayName: proto.String("Ana Prueba"), Vcard: proto.String(vcard),
	}}, &pm)
	if pm.Text != "Contact: Ana Prueba (+51 900 000 001)" {
		t.Fatalf("Text = %q", pm.Text)
	}
	if len(pm.Contacts) != 1 || pm.Contacts[0].VCard != vcard || pm.Contacts[0].DisplayName != "Ana Prueba" {
		t.Fatalf("Contacts = %+v", pm.Contacts)
	}
}

func TestExtractContactsArrayKeepsEveryCardInOrder(t *testing.T) {
	first := "BEGIN:VCARD\nFN:Uno\nTEL;waid=51900000001:+51 900 000 001\nTEL;type=WORK:+51 1 234 5678\nEND:VCARD"
	second := "BEGIN:VCARD\nFN:Dos\nEND:VCARD"
	var pm ParsedMessage
	extractContactText(&waProto.Message{ContactsArrayMessage: &waProto.ContactsArrayMessage{
		DisplayName: proto.String("2 contacts"),
		Contacts: []*waProto.ContactMessage{
			{DisplayName: proto.String("Uno"), Vcard: proto.String(first)},
			nil,
			{DisplayName: proto.String("Sin vcard")},
			{DisplayName: proto.String("Dos"), Vcard: proto.String(second)},
		},
	}}, &pm)
	if len(pm.Contacts) != 2 || pm.Contacts[0].VCard != first || pm.Contacts[1].VCard != second {
		t.Fatalf("Contacts = %+v", pm.Contacts)
	}
	if pm.Text != "Contacts:\nContact: Uno (+51 900 000 001, +51 1 234 5678)\nContact: Sin vcard\nContact: Dos" {
		t.Fatalf("Text = %q", pm.Text)
	}
}

func TestExtractContactWithoutTelStillKeepsTheCard(t *testing.T) {
	vcard := "BEGIN:VCARD\nFN:Sin Numero\nEND:VCARD"
	var pm ParsedMessage
	extractContactText(&waProto.Message{ContactMessage: &waProto.ContactMessage{Vcard: proto.String(vcard)}}, &pm)
	if pm.Text != "Contact: Sin Numero" || len(pm.Contacts) != 1 {
		t.Fatalf("Text = %q Contacts = %+v", pm.Text, pm.Contacts)
	}
}

func TestExtractContactWithoutVCardStoresNoCard(t *testing.T) {
	var pm ParsedMessage
	extractContactText(&waProto.Message{ContactMessage: &waProto.ContactMessage{DisplayName: proto.String("Solo nombre")}}, &pm)
	if pm.Text != "Contact: Solo nombre" || len(pm.Contacts) != 0 {
		t.Fatalf("Text = %q Contacts = %+v", pm.Text, pm.Contacts)
	}
}

// WhatsApp can send the number only as the waid parameter, with an empty value.
func TestContactDisplayTextFallsBackToWaidWhenTelValueIsEmpty(t *testing.T) {
	got := contactDisplayText(&waProto.ContactMessage{Vcard: proto.String(
		"BEGIN:VCARD\nFN:Ana Prueba\nTEL;type=CELL;waid=51900000001:\nEND:VCARD")})
	if got != "Contact: Ana Prueba (+51900000001)" {
		t.Fatalf("contactDisplayText = %q", got)
	}
}
