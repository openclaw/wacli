package wa

import (
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"
)

// Every group message repeats the sender's label in its context info.
func TestParseLiveMessageKeepsSenderMemberLabel(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		ExtendedTextMessage: &waProto.ExtendedTextMessage{
			Text: proto.String("see you at training"),
			ContextInfo: &waProto.ContextInfo{
				MemberLabel: &waE2E.MemberLabel{
					Label:          proto.String(" Mom of Alex "),
					LabelTimestamp: proto.Int64(1767225600),
				},
			},
		},
	}))
	if pm.Text != "see you at training" {
		t.Fatalf("text = %q", pm.Text)
	}
	if pm.MemberLabel == nil || pm.MemberLabel.Label != "Mom of Alex" {
		t.Fatalf("member label = %+v, want Mom of Alex", pm.MemberLabel)
	}
	if want := time.Unix(1767225600, 0).UTC(); !pm.MemberLabel.SetAt.Equal(want) {
		t.Fatalf("set at = %v, want %v", pm.MemberLabel.SetAt, want)
	}
	if pm.MemberLabelChange {
		t.Fatal("an ordinary message is not a label change")
	}
}

// A label change is a protocol message without content; it must neither be
// stored as an empty message nor reported as an unhandled payload.
func TestParseLiveMessageMemberLabelChange(t *testing.T) {
	pm := ParseLiveMessage(liveEvent(&waProto.Message{
		ProtocolMessage: &waProto.ProtocolMessage{
			Type: waE2E.ProtocolMessage_GROUP_MEMBER_LABEL_CHANGE.Enum(),
			MemberLabel: &waE2E.MemberLabel{
				Label:          proto.String("Coach"),
				LabelTimestamp: proto.Int64(1767225600123),
			},
		},
	}))
	if !pm.MemberLabelChange || pm.MemberLabel == nil || pm.MemberLabel.Label != "Coach" {
		t.Fatalf("label change = %v, label = %+v", pm.MemberLabelChange, pm.MemberLabel)
	}
	if want := time.UnixMilli(1767225600123).UTC(); !pm.MemberLabel.SetAt.Equal(want) {
		t.Fatalf("set at = %v, want %v (milliseconds)", pm.MemberLabel.SetAt, want)
	}
	if pm.HasContent() {
		t.Fatal("a label change has no content")
	}
	if pm.UnhandledPayload != "" {
		t.Fatalf("unhandled payload = %q, want none", pm.UnhandledPayload)
	}
}

// A cleared label is an empty label, which differs from carrying none.
func TestMemberLabelFromProto(t *testing.T) {
	if MemberLabelFromProto(nil) != nil {
		t.Fatal("no label must stay nil")
	}
	cleared := MemberLabelFromProto(&waE2E.MemberLabel{})
	if cleared == nil || cleared.Label != "" || !cleared.SetAt.IsZero() {
		t.Fatalf("cleared = %+v, want empty label with unknown time", cleared)
	}
}
