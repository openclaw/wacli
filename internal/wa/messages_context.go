package wa

import (
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

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
	if tmpl := m.GetTemplateMessage(); tmpl != nil {
		return tmpl.GetContextInfo()
	}
	if btn := m.GetButtonsMessage(); btn != nil {
		return btn.GetContextInfo()
	}
	if resp := m.GetButtonsResponseMessage(); resp != nil {
		return resp.GetContextInfo()
	}
	if im := m.GetInteractiveMessage(); im != nil {
		return im.GetContextInfo()
	}
	if resp := m.GetInteractiveResponseMessage(); resp != nil {
		return resp.GetContextInfo()
	}
	if list := m.GetListMessage(); list != nil {
		return list.GetContextInfo()
	}
	if lr := m.GetListResponseMessage(); lr != nil {
		return lr.GetContextInfo()
	}
	if tbr := m.GetTemplateButtonReplyMessage(); tbr != nil {
		return tbr.GetContextInfo()
	}
	if creation := pickPollCreation(m); creation != nil {
		return creation.GetContextInfo()
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
		return contactDisplayText(contact)
	}
	if contacts := m.GetContactsArrayMessage(); contacts != nil {
		return contactsArrayDisplayText(contacts)
	}
	if creation := pickPollCreation(m); creation != nil {
		if q := strings.TrimSpace(creation.GetName()); q != "" {
			return "Poll: " + q
		}
		return "Poll"
	}
	if m.GetPollUpdateMessage() != nil {
		return "Poll vote"
	}
	if m.GetPollAddOptionMessage() != nil {
		return "Poll option added"
	}
	if secret := m.GetSecretEncryptedMessage(); secret != nil && secret.GetSecretEncType() == waE2E.SecretEncryptedMessage_POLL_ADD_OPTION {
		return "Poll option added"
	}

	if text := strings.TrimSpace(m.GetConversation()); text != "" {
		return text
	}
	if ext := m.GetExtendedTextMessage(); ext != nil {
		if text := strings.TrimSpace(ext.GetText()); text != "" {
			return text
		}
	}
	if tmpl := m.GetTemplateMessage(); tmpl != nil {
		if h := hydratedTemplate(tmpl); h != nil {
			if t := strings.TrimSpace(h.GetHydratedContentText()); t != "" {
				return t
			}
		}
	}
	if btn := m.GetButtonsMessage(); btn != nil {
		if t := strings.TrimSpace(btn.GetContentText()); t != "" {
			return t
		}
	}
	if resp := m.GetButtonsResponseMessage(); resp != nil {
		return resp.GetSelectedDisplayText()
	}
	if im := m.GetInteractiveMessage(); im != nil {
		return interactiveText(im)
	}
	if list := m.GetListMessage(); list != nil {
		if t := strings.TrimSpace(list.GetDescription()); t != "" {
			return t
		}
	}
	if lr := m.GetListResponseMessage(); lr != nil {
		return strings.TrimSpace(lr.GetTitle())
	}
	if tbr := m.GetTemplateButtonReplyMessage(); tbr != nil {
		return tbr.GetSelectedDisplayText()
	}
	return ""
}
