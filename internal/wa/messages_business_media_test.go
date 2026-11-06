package wa

import (
	"bytes"
	"testing"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"google.golang.org/protobuf/proto"
)

func TestParseBusinessHeaderMedia(t *testing.T) {
	doc := &waProto.DocumentMessage{
		Caption: proto.String("attachment caption"), FileName: proto.String("invoice.pdf"),
		Mimetype: proto.String("application/pdf"), DirectPath: proto.String("/synthetic/document"),
		MediaKey: []byte{1, 2, 3}, FileSHA256: []byte{4, 5, 6}, FileEncSHA256: []byte{7, 8, 9}, FileLength: proto.Uint64(123),
	}
	hydrated := &waProto.TemplateMessage_HydratedFourRowTemplate{
		Title:               &waProto.TemplateMessage_HydratedFourRowTemplate_DocumentMessage{DocumentMessage: doc},
		HydratedContentText: proto.String("Invoice ready"), HydratedFooterText: proto.String("Thank you"),
		HydratedButtons: []*waProto.HydratedTemplateButton{{HydratedButton: &waProto.HydratedTemplateButton_QuickReplyButton{
			QuickReplyButton: &waProto.HydratedTemplateButton_HydratedQuickReplyButton{DisplayText: proto.String("Paid"), ID: proto.String("paid")},
		}}},
	}
	interactive := &waProto.InteractiveMessage{
		Header: &waProto.InteractiveMessage_Header{Title: proto.String("Invoice"), Media: &waProto.InteractiveMessage_Header_DocumentMessage{DocumentMessage: doc}},
		Body:   &waProto.InteractiveMessage_Body{Text: proto.String("Ready")}, Footer: &waProto.InteractiveMessage_Footer{Text: proto.String("Thank you")},
	}
	image := &waProto.ImageMessage{Mimetype: proto.String("image/jpeg"), DirectPath: proto.String("/synthetic/image"), MediaKey: doc.MediaKey, FileSHA256: doc.FileSHA256, FileEncSHA256: doc.FileEncSHA256, FileLength: doc.FileLength}
	video := &waProto.VideoMessage{Mimetype: proto.String("video/mp4"), DirectPath: proto.String("/synthetic/video"), MediaKey: doc.MediaKey, FileSHA256: doc.FileSHA256, FileEncSHA256: doc.FileEncSHA256, FileLength: doc.FileLength}
	cases := []struct {
		name, text, kind, mime, path, filename string
		message                                *waProto.Message
		buttons                                int
	}{
		{"hydrated field", "Invoice ready\n[Thank you]", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: hydrated}}, 1},
		{"hydrated format", "Invoice ready\n[Thank you]", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{Format: &waProto.TemplateMessage_HydratedFourRowTemplate_{HydratedFourRowTemplate: hydrated}}}, 1},
		{"four row", "attachment caption", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{Format: &waProto.TemplateMessage_FourRowTemplate_{FourRowTemplate: &waProto.TemplateMessage_FourRowTemplate{Title: &waProto.TemplateMessage_FourRowTemplate_DocumentMessage{DocumentMessage: doc}}}}}, 0},
		{"interactive", "Invoice\nReady\n[Thank you]", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{InteractiveMessage: interactive}, 0},
		{"interactive template", "Invoice\nReady\n[Thank you]", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{Format: &waProto.TemplateMessage_InteractiveMessageTemplate{InteractiveMessageTemplate: interactive}}}, 0},
		{"buttons", "Invoice ready", "document", "application/pdf", "/synthetic/document", "invoice.pdf", &waProto.Message{ButtonsMessage: &waProto.ButtonsMessage{ContentText: proto.String("Invoice ready"), Header: &waProto.ButtonsMessage_DocumentMessage{DocumentMessage: doc}}}, 0},
		{"image", "Image ready", "image", "image/jpeg", "/synthetic/image", "", &waProto.Message{TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{HydratedContentText: proto.String("Image ready"), Title: &waProto.TemplateMessage_HydratedFourRowTemplate_ImageMessage{ImageMessage: image}}}}, 0},
		{"video", "Video ready", "video", "video/mp4", "/synthetic/video", "", &waProto.Message{InteractiveMessage: &waProto.InteractiveMessage{Body: &waProto.InteractiveMessage_Body{Text: proto.String("Video ready")}, Header: &waProto.InteractiveMessage_Header{Media: &waProto.InteractiveMessage_Header_VideoMessage{VideoMessage: video}}}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []string{"live", "history"} {
				t.Run(source, func(t *testing.T) {
					pm := ParseLiveMessage(liveEvent(tc.message))
					if source == "history" {
						pm = ParseHistoryMessage("123@s.whatsapp.net", &waProto.WebMessageInfo{Key: &waProto.MessageKey{ID: proto.String("header")}, Message: tc.message})
					}
					if pm.Text != tc.text || len(pm.Buttons) != tc.buttons {
						t.Fatalf("text/buttons changed: %q / %v", pm.Text, pm.Buttons)
					}
					m := pm.Media
					if m == nil {
						t.Fatal("business header attachment was dropped")
					}
					if m.Type != tc.kind || m.MimeType != tc.mime || m.DirectPath != tc.path || m.Filename != tc.filename || m.FileLength != 123 {
						t.Fatalf("unexpected media: %+v", m)
					}
					if !bytes.Equal(m.MediaKey, doc.MediaKey) || !bytes.Equal(m.FileSHA256, doc.FileSHA256) || !bytes.Equal(m.FileEncSHA256, doc.FileEncSHA256) {
						t.Fatal("attachment retrieval bytes changed")
					}
					m.MediaKey[0] = 99
					if doc.MediaKey[0] != 1 {
						t.Fatal("parsed key aliases the protobuf")
					}
				})
			}
		})
	}
}

func TestBusinessHeaderKeepsTopLevelMedia(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		DocumentMessage: &waProto.DocumentMessage{FileName: proto.String("outer.pdf")},
		TemplateMessage: &waProto.TemplateMessage{HydratedTemplate: &waProto.TemplateMessage_HydratedFourRowTemplate{
			HydratedContentText: proto.String("Body"),
			Title:               &waProto.TemplateMessage_HydratedFourRowTemplate_DocumentMessage{DocumentMessage: &waProto.DocumentMessage{FileName: proto.String("inner.pdf")}},
		}},
	}))
	if pm.Media == nil || pm.Media.Filename != "outer.pdf" || pm.Text != "Body" {
		t.Fatalf("top-level media precedence changed: %+v", pm)
	}
}
