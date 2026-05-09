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

type Button struct {
	Type        string `json:"type"`
	DisplayText string `json:"display_text"`
	ID          string `json:"id,omitempty"`
	URL         string `json:"url,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Description string `json:"description,omitempty"`
}

// Poll captures the question + option list extracted from an incoming
// PollCreationMessage (any of V1/V2/V3/V4/V5).
type Poll struct {
	Question        string
	Options         []string
	SelectableCount uint32
}

// PollVoteRef references the original poll a PollUpdateMessage is voting on.
// Decryption of the vote happens later in the sync handler.
type PollVoteRef struct {
	PollMessageID string
	PollChatJID   string
	PollSenderJID string
}

type ParsedMessage struct {
	Chat            types.JID
	ID              string
	SenderJID       string
	Timestamp       time.Time
	FromMe          bool
	Text            string
	Buttons         []Button
	Media           *Media
	Poll            *Poll
	PollVote        *PollVoteRef
	PushName        string
	ReplyToID       string
	ReplyToDisplay  string
	ReactionToID    string
	ReactionEmoji   string
	IsForwarded     bool
	ForwardingScore uint32
	StarredKnown    bool
	Starred         bool
	Revoked         bool
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
	if hist.Starred != nil {
		pm.StarredKnown = true
		pm.Starred = hist.GetStarred()
	}

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

	if extractProtocolMutation(m, pm) {
		return
	}
	extractReaction(m, pm)
	extractPlainText(m, pm)
	extractMedia(m, pm)
	extractContactText(m, pm)
	extractBusinessText(m, pm)
	extractPoll(m, pm)
	extractPollUpdate(m, pm)

	if ctx := contextInfoForMessage(m); ctx != nil {
		if id := strings.TrimSpace(ctx.GetStanzaID()); id != "" {
			pm.ReplyToID = id
		}
		if quoted := ctx.GetQuotedMessage(); quoted != nil {
			pm.ReplyToDisplay = strings.TrimSpace(displayTextForProto(quoted))
		}
		pm.ForwardingScore = ctx.GetForwardingScore()
		pm.IsForwarded = ctx.GetIsForwarded() || pm.ForwardingScore > 0
	}
}

func extractProtocolMutation(m *waProto.Message, pm *ParsedMessage) bool {
	protocol := m.GetProtocolMessage()
	if protocol == nil {
		return false
	}
	switch protocol.GetType() {
	case waProto.ProtocolMessage_REVOKE:
		key := protocol.GetKey()
		if key == nil {
			return false
		}
		if id := strings.TrimSpace(key.GetID()); id != "" {
			pm.ID = id
		}
		if remote := strings.TrimSpace(key.GetRemoteJID()); remote != "" {
			if chat, err := types.ParseJID(remote); err == nil {
				pm.Chat = chat
			}
		}
		if participant := strings.TrimSpace(key.GetParticipant()); participant != "" {
			pm.SenderJID = participant
		}
		pm.FromMe = key.GetFromMe()
		pm.Text = ""
		pm.Media = nil
		pm.Revoked = true
		return true
	default:
		return false
	}
}

func extractReaction(m *waProto.Message, pm *ParsedMessage) {
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
}

func extractPoll(m *waProto.Message, pm *ParsedMessage) {
	creation := pickPollCreation(m)
	if creation == nil {
		return
	}
	question := strings.TrimSpace(creation.GetName())
	options := make([]string, 0, len(creation.GetOptions()))
	for _, opt := range creation.GetOptions() {
		options = append(options, opt.GetOptionName())
	}
	pm.Poll = &Poll{
		Question:        question,
		Options:         options,
		SelectableCount: creation.GetSelectableOptionsCount(),
	}
	if pm.Text == "" && question != "" {
		pm.Text = "Poll: " + question
	}
}

// pickPollCreation returns the inner PollCreationMessage from any of the
// known V1/V2/V3/V4/V5 fields, including the FutureProofMessage wrappers.
func pickPollCreation(m *waProto.Message) *waProto.PollCreationMessage {
	if m == nil {
		return nil
	}
	if c := m.GetPollCreationMessage(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV2(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV3(); c != nil {
		return c
	}
	if c := m.GetPollCreationMessageV5(); c != nil {
		return c
	}
	if fp := m.GetPollCreationMessageV4(); fp != nil {
		if inner := fp.GetMessage(); inner != nil {
			if c := inner.GetPollCreationMessage(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV2(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV3(); c != nil {
				return c
			}
			if c := inner.GetPollCreationMessageV5(); c != nil {
				return c
			}
		}
	}
	return nil
}

func extractPollUpdate(m *waProto.Message, pm *ParsedMessage) {
	update := m.GetPollUpdateMessage()
	if update == nil {
		return
	}
	key := update.GetPollCreationMessageKey()
	ref := &PollVoteRef{}
	if key != nil {
		ref.PollMessageID = strings.TrimSpace(key.GetID())
		ref.PollChatJID = strings.TrimSpace(key.GetRemoteJID())
		ref.PollSenderJID = strings.TrimSpace(key.GetParticipant())
	}
	pm.PollVote = ref
	if pm.Text == "" {
		pm.Text = "Poll vote"
	}
}

func extractPlainText(m *waProto.Message, pm *ParsedMessage) {
	switch {
	case m.GetConversation() != "":
		pm.Text = m.GetConversation()
	case m.GetExtendedTextMessage() != nil:
		pm.Text = m.GetExtendedTextMessage().GetText()
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
