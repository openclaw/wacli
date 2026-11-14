package wa

import (
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

// MemberLabel is the label a group member shows under their name in one group,
// for example "Team lead". An empty Label means the member cleared it.
type MemberLabel struct {
	Label string
	// SetAt is when the member set the label. Zero when WhatsApp did not say.
	SetAt time.Time
}

// MemberLabelFromProto converts the protocol label. It returns nil when the
// message carried no label at all, which is different from a cleared label.
func MemberLabelFromProto(label *waE2E.MemberLabel) *MemberLabel {
	if label == nil {
		return nil
	}
	return &MemberLabel{
		Label: strings.TrimSpace(label.GetLabel()),
		SetAt: memberLabelTime(label.GetLabelTimestamp()),
	}
}

// memberLabelTime accepts seconds or milliseconds; the protocol does not
// document the unit and both appear in WhatsApp timestamps.
func memberLabelTime(ts int64) time.Time {
	switch {
	case ts <= 0:
		return time.Time{}
	case ts >= 1e12:
		return time.UnixMilli(ts).UTC()
	default:
		return time.Unix(ts, 0).UTC()
	}
}
