package wa

import (
	"strings"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type Media struct {
	Type          string
	Caption       string
	Filename      string
	MimeType      string
	DirectPath    string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
}

type ParsedMessage struct {
	Chat           types.JID
	ID             string
	SenderJID      string
	Timestamp      time.Time
	FromMe         bool
	Text           string
	Media          *Media
	PushName       string
	ReplyToID      string
	ReplyToDisplay string
	ReactionToID   string
	ReactionEmoji  string
}

func ParseLiveMessage(evt *events.Message) ParsedMessage {
	msg := ParsedMessage{
		Chat:      evt.Info.Chat,
		ID:        evt.Info.ID,
		Timestamp: evt.Info.Timestamp,
		FromMe:    evt.Info.IsFromMe,
		PushName:  evt.Info.PushName,
	}
	if s := evt.Info.Sender.String(); s != "" {
		msg.SenderJID = s
	}

	extractWAProto(evt.Message, &msg)
	return msg
}

func ParseHistoryMessage(chatJID string, hist *waProto.WebMessageInfo) ParsedMessage {
	var chat types.JID
	if parsed, err := types.ParseJID(chatJID); err == nil {
		chat = parsed
	}

	pm := ParsedMessage{
		Chat:      chat,
		ID:        hist.GetKey().GetID(),
		Timestamp: time.Unix(int64(hist.GetMessageTimestamp()), 0).UTC(),
		FromMe:    hist.GetKey().GetFromMe(),
	}

	// Prefer top-level participant (works for LID-addressed groups),
	// then fall back to key.participant, then key.remoteJID.
	// This matches whatsmeow's own ParseWebMessage priority.
	sender := strings.TrimSpace(hist.GetParticipant())
	if sender == "" {
		sender = strings.TrimSpace(hist.GetKey().GetParticipant())
	}
	if sender == "" {
		sender = strings.TrimSpace(hist.GetKey().GetRemoteJID())
	}
	pm.SenderJID = sender

	if hist.GetMessage() != nil {
		extractWAProto(hist.GetMessage(), &pm)
	}
	return pm
}

func extractWAProto(m *waProto.Message, pm *ParsedMessage) {
	if m == nil || pm == nil {
		return
	}

	if reaction := m.GetReactionMessage(); reaction != nil {
		pm.ReactionEmoji = reaction.GetText()
		if key := reaction.GetKey(); key != nil {
			pm.ReactionToID = key.GetID()
		}
	} else if encReaction := m.GetEncReactionMessage(); encReaction != nil {
		if key := encReaction.GetTargetMessageKey(); key != nil {
			pm.ReactionToID = key.GetID()
		}
	}

	switch {
	case m.GetConversation() != "":
		pm.Text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		pm.Text = m.GetExtendedTextMessage().GetText()
	}

	if img := m.GetImageMessage(); img != nil {
		if pm.Text == "" {
			pm.Text = img.GetCaption()
		}
		pm.Media = &Media{
			Type:          "image",
			Caption:       img.GetCaption(),
			MimeType:      img.GetMimetype(),
			DirectPath:    img.GetDirectPath(),
			MediaKey:      clone(img.GetMediaKey()),
			FileSHA256:    clone(img.GetFileSHA256()),
			FileEncSHA256: clone(img.GetFileEncSHA256()),
			FileLength:    img.GetFileLength(),
		}
	}

	if vid := m.GetVideoMessage(); vid != nil {
		if pm.Text == "" {
			pm.Text = vid.GetCaption()
		}
		mediaType := "video"
		if vid.GetGifPlayback() {
			mediaType = "gif"
		}
		pm.Media = &Media{
			Type:          mediaType,
			Caption:       vid.GetCaption(),
			MimeType:      vid.GetMimetype(),
			DirectPath:    vid.GetDirectPath(),
			MediaKey:      clone(vid.GetMediaKey()),
			FileSHA256:    clone(vid.GetFileSHA256()),
			FileEncSHA256: clone(vid.GetFileEncSHA256()),
			FileLength:    vid.GetFileLength(),
		}
	}

	if aud := m.GetAudioMessage(); aud != nil {
		if pm.Text == "" {
			pm.Text = "[Audio]"
		}
		pm.Media = &Media{
			Type:          "audio",
			Caption:       pm.Text,
			MimeType:      aud.GetMimetype(),
			DirectPath:    aud.GetDirectPath(),
			MediaKey:      clone(aud.GetMediaKey()),
			FileSHA256:    clone(aud.GetFileSHA256()),
			FileEncSHA256: clone(aud.GetFileEncSHA256()),
			FileLength:    aud.GetFileLength(),
		}
	}

	if doc := m.GetDocumentMessage(); doc != nil {
		if pm.Text == "" {
			pm.Text = doc.GetCaption()
		}
		pm.Media = &Media{
			Type:          "document",
			Caption:       doc.GetCaption(),
			Filename:      doc.GetFileName(),
			MimeType:      doc.GetMimetype(),
			DirectPath:    doc.GetDirectPath(),
			MediaKey:      clone(doc.GetMediaKey()),
			FileSHA256:    clone(doc.GetFileSHA256()),
			FileEncSHA256: clone(doc.GetFileEncSHA256()),
			FileLength:    doc.GetFileLength(),
		}
	}

	if sticker := m.GetStickerMessage(); sticker != nil {
		pm.Media = &Media{
			Type:          "sticker",
			MimeType:      sticker.GetMimetype(),
			DirectPath:    sticker.GetDirectPath(),
			MediaKey:      clone(sticker.GetMediaKey()),
			FileSHA256:    clone(sticker.GetFileSHA256()),
			FileEncSHA256: clone(sticker.GetFileEncSHA256()),
			FileLength:    sticker.GetFileLength(),
		}
	}

	if ctx := contextInfoForMessage(m); ctx != nil {
		if id := strings.TrimSpace(ctx.GetStanzaID()); id != "" {
			pm.ReplyToID = id
		}
		if quoted := ctx.GetQuotedMessage(); quoted != nil {
			pm.ReplyToDisplay = strings.TrimSpace(displayTextForProto(quoted))
		}
	}
}

func clone(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func contextInfoForMessage(m *waProto.Message) *waProto.ContextInfo {
	if m == nil {
		return nil
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		return ext.GetContextInfo()
	}
	if img := m.GetImageMessage(); img != nil {
		return img.GetContextInfo()
	}
	if vid := m.GetVideoMessage(); vid != nil {
		return vid.GetContextInfo()
	}
	if aud := m.GetAudioMessage(); aud != nil {
		return aud.GetContextInfo()
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		return doc.GetContextInfo()
	}
	if sticker := m.GetStickerMessage(); sticker != nil {
		return sticker.GetContextInfo()
	}
	if loc := m.GetLocationMessage(); loc != nil {
		return loc.GetContextInfo()
	}
	if contact := m.GetContactMessage(); contact != nil {
		return contact.GetContextInfo()
	}
	if contacts := m.GetContactsArrayMessage(); contacts != nil {
		return contacts.GetContextInfo()
	}
	return nil
}

func displayTextForProto(m *waProto.Message) string {
	if m == nil {
		return ""
	}

	if img := m.GetImageMessage(); img != nil {
		return "Sent image"
	}
	if vid := m.GetVideoMessage(); vid != nil {
		if vid.GetGifPlayback() {
			return "Sent gif"
		}
		return "Sent video"
	}
	if aud := m.GetAudioMessage(); aud != nil {
		return "Sent audio"
	}
	if doc := m.GetDocumentMessage(); doc != nil {
		return "Sent document"
	}
	if sticker := m.GetStickerMessage(); sticker != nil {
		return "Sent sticker"
	}
	if loc := m.GetLocationMessage(); loc != nil {
		return "Sent location"
	}
	if contact := m.GetContactMessage(); contact != nil {
		return "Sent contact"
	}
	if contacts := m.GetContactsArrayMessage(); contacts != nil {
		return "Sent contacts"
	}

	if text := strings.TrimSpace(m.GetConversation()); text != "" {
		return text
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		if text := strings.TrimSpace(ext.GetText()); text != "" {
			return text
		}
	}
	return ""
}
