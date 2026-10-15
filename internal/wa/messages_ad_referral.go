package wa

import (
	"strings"
	"unicode/utf8"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waE2E"
)

const adReferralFieldLimit = 1024

// AdReferral is the ad attribution WhatsApp attaches to the first message a
// person sends after tapping a Click-to-WhatsApp ad.
type AdReferral struct {
	SourceType       string `json:"source_type,omitempty"`
	SourceID         string `json:"source_id,omitempty"`
	SourceURL        string `json:"source_url,omitempty"`
	SourceApp        string `json:"source_app,omitempty"`
	Title            string `json:"title,omitempty"`
	Body             string `json:"body,omitempty"`
	MediaType        string `json:"media_type,omitempty"`
	CtwaClid         string `json:"ctwa_clid,omitempty"`
	ConversionSource string `json:"conversion_source,omitempty"`
	EntryPointSource string `json:"entry_point_source,omitempty"`
	EntryPointApp    string `json:"entry_point_app,omitempty"`
}

func adReferralFromContext(ctx *waProto.ContextInfo) *AdReferral {
	ad := ctx.GetExternalAdReply()
	ref := AdReferral{
		SourceType:       adReferralField(ad.GetSourceType()),
		SourceID:         adReferralField(ad.GetSourceID()),
		SourceURL:        adReferralField(ad.GetSourceURL()),
		SourceApp:        adReferralField(ad.GetSourceApp()),
		Title:            adReferralField(ad.GetTitle()),
		Body:             adReferralField(ad.GetBody()),
		MediaType:        adReferralMediaType(ad.GetMediaType()),
		CtwaClid:         adReferralField(ad.GetCtwaClid()),
		ConversionSource: adReferralField(ctx.GetConversionSource()),
		EntryPointSource: adReferralField(ctx.GetEntryPointConversionSource()),
		EntryPointApp:    adReferralField(ctx.GetEntryPointConversionApp()),
	}
	if ref == (AdReferral{}) {
		return nil
	}
	return &ref
}

func adReferralMediaType(mt waE2E.ContextInfo_ExternalAdReplyInfo_MediaType) string {
	if mt == waE2E.ContextInfo_ExternalAdReplyInfo_NONE {
		return ""
	}
	return strings.ToLower(mt.String())
}

func adReferralField(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= adReferralFieldLimit {
		return s
	}
	cut := adReferralFieldLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
