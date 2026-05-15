package wa

import (
	"fmt"
	"strings"
	"time"

	waBinary "go.mau.fi/whatsmeow/binary"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type ParsedCallParticipant struct {
	JID     string
	Outcome string
}

type ParsedCallEvent struct {
	Chat         types.JID
	SenderJID    string
	CallID       string
	MsgID        string
	EventType    string
	Direction    string
	Media        string
	Outcome      string
	Reason       string
	CallType     string
	DurationSecs int64
	Timestamp    time.Time
	Participants []ParsedCallParticipant
}

type ParsedCallDelete struct {
	Chat      types.JID
	Direction string
}

func extractCallLog(m *waProto.Message, pm *ParsedMessage) {
	call := m.GetCallLogMesssage()
	if call == nil {
		return
	}
	pm.Call = &ParsedCallEvent{
		Chat:         pm.Chat,
		SenderJID:    pm.SenderJID,
		CallID:       pm.ID,
		MsgID:        pm.ID,
		EventType:    "call_log",
		Direction:    directionFromFromMe(pm.FromMe),
		Media:        audioVideo(call.GetIsVideo()),
		Outcome:      callLogMessageOutcome(call.GetCallOutcome()),
		CallType:     callLogMessageType(call.GetCallType()),
		DurationSecs: call.GetDurationSecs(),
		Timestamp:    pm.Timestamp,
		Participants: callLogMessageParticipants(call.GetParticipants()),
	}
}

func ParseLiveCallEvent(evt interface{}, self types.JID) (ParsedCallEvent, bool) {
	switch v := evt.(type) {
	case *events.CallOffer:
		return callEventFromMeta(v.BasicCallMeta, self, "offer", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.CallAccept:
		return callEventFromMeta(v.BasicCallMeta, self, "accept", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.CallPreAccept:
		return callEventFromMeta(v.BasicCallMeta, self, "pre_accept", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.CallTransport:
		return callEventFromMeta(v.BasicCallMeta, self, "transport", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.CallOfferNotice:
		return callEventFromMeta(v.BasicCallMeta, self, "offer_notice", "", "", cleanCallValue(v.Media), cleanCallValue(v.Type)), true
	case *events.CallRelayLatency:
		return callEventFromMeta(v.BasicCallMeta, self, "relay_latency", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.CallTerminate:
		return callEventFromMeta(v.BasicCallMeta, self, "terminate", "", cleanCallValue(v.Reason), mediaFromCallNode(v.Data), ""), true
	case *events.CallReject:
		return callEventFromMeta(v.BasicCallMeta, self, "reject", "", "", mediaFromCallNode(v.Data), ""), true
	case *events.AppState:
		return parseCallLogAppState(v, self)
	default:
		return ParsedCallEvent{}, false
	}
}

func ParseCallLogDeleteEvent(evt interface{}) (ParsedCallDelete, bool) {
	v, ok := evt.(*events.AppState)
	if !ok || v == nil || v.SyncActionValue == nil || v.GetDeleteIndividualCallLog() == nil {
		return ParsedCallDelete{}, false
	}
	action := v.GetDeleteIndividualCallLog()
	peer := strings.TrimSpace(action.GetPeerJID())
	if peer == "" {
		return ParsedCallDelete{}, false
	}
	chat, err := types.ParseJID(peer)
	if err != nil || chat.IsEmpty() {
		return ParsedCallDelete{}, false
	}
	return ParsedCallDelete{
		Chat:      chat,
		Direction: directionFromIncoming(action.GetIsIncoming()),
	}, true
}

func callEventFromMeta(meta types.BasicCallMeta, self types.JID, eventType, outcome, reason, media, callType string) ParsedCallEvent {
	chat := meta.From
	if !meta.GroupJID.IsEmpty() {
		chat = meta.GroupJID
	}
	sender := meta.From.String()
	if !meta.CallCreator.IsEmpty() {
		sender = meta.CallCreator.String()
	}
	return ParsedCallEvent{
		Chat:      chat,
		SenderJID: sender,
		CallID:    strings.TrimSpace(meta.CallID),
		EventType: eventType,
		Direction: directionFromCallCreator(meta.CallCreator, self, eventType),
		Media:     cleanCallValue(media),
		Outcome:   cleanCallValue(outcome),
		Reason:    cleanCallValue(reason),
		CallType:  cleanCallValue(callType),
		Timestamp: meta.Timestamp,
	}
}

func parseCallLogAppState(evt *events.AppState, self types.JID) (ParsedCallEvent, bool) {
	if evt == nil || evt.SyncActionValue == nil || evt.GetCallLogAction() == nil || evt.GetCallLogAction().GetCallLogRecord() == nil {
		return ParsedCallEvent{}, false
	}
	record := evt.GetCallLogAction().GetCallLogRecord()
	chat, sender := callLogRecordChatAndSender(record, self)
	if chat.IsEmpty() {
		return ParsedCallEvent{}, false
	}
	ts := time.UnixMilli(record.GetStartTime()).UTC()
	if record.GetStartTime() <= 0 && evt.GetTimestamp() > 0 {
		ts = time.UnixMilli(evt.GetTimestamp()).UTC()
	}
	return ParsedCallEvent{
		Chat:         chat,
		SenderJID:    sender,
		CallID:       strings.TrimSpace(record.GetCallID()),
		EventType:    "call_log",
		Direction:    directionFromIncoming(record.GetIsIncoming()),
		Media:        audioVideo(record.GetIsVideo()),
		Outcome:      callLogRecordResult(record.GetCallResult()),
		Reason:       callLogRecordReason(record),
		CallType:     callLogRecordType(record.GetCallType()),
		DurationSecs: record.GetDuration(),
		Timestamp:    ts,
		Participants: callLogRecordParticipants(record.GetParticipants()),
	}, true
}

func callLogRecordChatAndSender(record *waSyncAction.CallLogRecord, self types.JID) (types.JID, string) {
	if group := strings.TrimSpace(record.GetGroupJID()); group != "" {
		if jid, err := types.ParseJID(group); err == nil {
			return jid, strings.TrimSpace(record.GetCallCreatorJID())
		}
	}
	creator := strings.TrimSpace(record.GetCallCreatorJID())
	if creator != "" {
		if creatorJID, err := types.ParseJID(creator); err == nil && (self.IsEmpty() || creatorJID.ToNonAD() != self.ToNonAD()) {
			return creatorJID, creator
		}
	}
	for _, p := range record.GetParticipants() {
		if p == nil || strings.TrimSpace(p.GetUserJID()) == "" {
			continue
		}
		if jid, err := types.ParseJID(p.GetUserJID()); err == nil && (self.IsEmpty() || jid.ToNonAD() != self.ToNonAD()) {
			return jid, creator
		}
	}
	if creator != "" {
		if jid, err := types.ParseJID(creator); err == nil {
			return jid, creator
		}
	}
	for _, p := range record.GetParticipants() {
		if p == nil || strings.TrimSpace(p.GetUserJID()) == "" {
			continue
		}
		if jid, err := types.ParseJID(p.GetUserJID()); err == nil {
			return jid, p.GetUserJID()
		}
	}
	return types.JID{}, ""
}

func directionFromCallCreator(creator, self types.JID, eventType string) string {
	if !creator.IsEmpty() && !self.IsEmpty() {
		if creator.ToNonAD() == self.ToNonAD() {
			return "outbound"
		}
		return "inbound"
	}
	switch eventType {
	case "offer", "offer_notice", "relay_latency":
		return "inbound"
	default:
		return "unknown"
	}
}

func directionFromFromMe(fromMe bool) string {
	if fromMe {
		return "outbound"
	}
	return "inbound"
}

func directionFromIncoming(incoming bool) string {
	if incoming {
		return "inbound"
	}
	return "outbound"
}

func audioVideo(isVideo bool) string {
	if isVideo {
		return "video"
	}
	return "audio"
}

func mediaFromCallNode(node *waBinary.Node) string {
	if node == nil {
		return ""
	}
	for _, key := range []string{"media", "type", "call-creator-media"} {
		if value, ok := node.Attrs[key]; ok {
			if s := cleanCallValue(fmt.Sprint(value)); s == "audio" || s == "video" {
				return s
			}
		}
	}
	for _, child := range node.GetChildren() {
		if s := cleanCallValue(child.Tag); s == "audio" || s == "video" {
			return s
		}
		if media := mediaFromCallNode(&child); media != "" {
			return media
		}
	}
	return ""
}

func cleanCallValue(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func callLogMessageOutcome(v waProto.CallLogMessage_CallOutcome) string {
	return cleanCallValue(v.String())
}

func callLogMessageType(v waProto.CallLogMessage_CallType) string {
	return cleanCallValue(v.String())
}

func callLogRecordResult(v waSyncAction.CallLogRecord_CallResult) string {
	value := cleanCallValue(v.String())
	if value == "acceptedelsewhere" {
		return "accepted_elsewhere"
	}
	return value
}

func callLogRecordType(v waSyncAction.CallLogRecord_CallType) string {
	return cleanCallValue(v.String())
}

func callLogRecordReason(record *waSyncAction.CallLogRecord) string {
	if record == nil {
		return ""
	}
	if reason := cleanCallValue(record.GetSilenceReason().String()); reason != "" && reason != "none" {
		return reason
	}
	if record.GetIsDndMode() {
		return "dnd"
	}
	return ""
}

func callLogMessageParticipants(participants []*waProto.CallLogMessage_CallParticipant) []ParsedCallParticipant {
	out := make([]ParsedCallParticipant, 0, len(participants))
	for _, p := range participants {
		if p == nil || strings.TrimSpace(p.GetJID()) == "" {
			continue
		}
		out = append(out, ParsedCallParticipant{
			JID:     strings.TrimSpace(p.GetJID()),
			Outcome: callLogMessageOutcome(p.GetCallOutcome()),
		})
	}
	return out
}

func callLogRecordParticipants(participants []*waSyncAction.CallLogRecord_ParticipantInfo) []ParsedCallParticipant {
	out := make([]ParsedCallParticipant, 0, len(participants))
	for _, p := range participants {
		if p == nil || strings.TrimSpace(p.GetUserJID()) == "" {
			continue
		}
		out = append(out, ParsedCallParticipant{
			JID:     strings.TrimSpace(p.GetUserJID()),
			Outcome: callLogRecordResult(p.GetCallResult()),
		})
	}
	return out
}
