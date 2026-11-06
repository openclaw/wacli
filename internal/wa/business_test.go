package wa

import (
	"reflect"
	"testing"

	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

func text(tag, value string) waBinary.Node {
	return waBinary.Node{Tag: tag, Content: []byte(value)}
}

func TestParseBusinessProfileNodeReadsEveryField(t *testing.T) {
	jid := types.NewJID("51905448483", types.DefaultUserServer)
	resp := &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{
		Tag: "business_profile", Attrs: waBinary.Attrs{"v": "244"},
		Content: []waBinary.Node{{
			Tag: "profile", Attrs: waBinary.Attrs{"jid": jid},
			Content: []waBinary.Node{
				text("address", "Av. Larco 1301"),
				text("description", "Cámaras y lentes"),
				text("email", "ventas@example.com"),
				text("website", "https://lenz.pe"),
				text("website", "https://tienda.lenz.pe"),
				{Tag: "categories", Content: []waBinary.Node{{Tag: "category", Attrs: waBinary.Attrs{"id": "1223"}, Content: []byte("Shopping & retail")}}},
				{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": "America/Lima"}, Content: []waBinary.Node{
					{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "mon", "mode": "specific_hours", "open_time": "540", "close_time": "1080"}},
					{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "sat", "mode": "open_24h"}},
				}},
				{Tag: "profile_options", Content: []waBinary.Node{text("cart_enabled", "true")}},
			},
		}},
	}}}
	got, err := ParseBusinessProfileNode(resp)
	if err != nil {
		t.Fatal(err)
	}
	want := &BusinessProfileFull{
		JID:                   "51905448483@s.whatsapp.net",
		Description:           "Cámaras y lentes",
		Address:               "Av. Larco 1301",
		Email:                 "ventas@example.com",
		Websites:              []string{"https://lenz.pe", "https://tienda.lenz.pe"},
		Categories:            []BusinessCategory{{ID: "1223", Name: "Shopping & retail"}},
		BusinessHoursTimeZone: "America/Lima",
		BusinessHours: []BusinessHoursDay{
			{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"},
			{DayOfWeek: "sat", Mode: "open_24h"},
		},
		ProfileOptions: map[string]string{"cart_enabled": "true"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestParseBusinessProfileNodeEmptyProfileHasNoNulls(t *testing.T) {
	resp := &waBinary.Node{Tag: "iq", Content: []waBinary.Node{{
		Tag: "business_profile", Content: []waBinary.Node{{Tag: "profile", Attrs: waBinary.Attrs{"jid": "1@s.whatsapp.net"}}},
	}}}
	got, err := ParseBusinessProfileNode(resp)
	if err != nil {
		t.Fatal(err)
	}
	if got.JID != "1@s.whatsapp.net" || got.Websites == nil || got.Categories == nil || got.BusinessHours == nil || got.ProfileOptions == nil {
		t.Fatalf("%+v", got)
	}
	if _, err := ParseBusinessProfileNode(&waBinary.Node{Tag: "iq"}); err == nil {
		t.Fatal("expected missing profile error")
	}
	if _, err := ParseBusinessProfileNode(nil); err == nil {
		t.Fatal("expected nil error")
	}
}

func TestBuildBusinessProfileUpdateNodesMatchesWhatsAppWeb(t *testing.T) {
	desc, email := "Nueva descripción", ""
	nodes := BuildBusinessProfileUpdateNodes(BusinessProfileUpdate{
		Description: &desc,
		Email:       &email,
		Websites:    []string{"https://lenz.pe"},
		Hours: &BusinessHoursUpdate{TimeZone: "America/Lima", Days: []BusinessHoursDay{
			{DayOfWeek: "mon", Mode: "specific_hours", OpenTime: "540", CloseTime: "1080"},
			{DayOfWeek: "sun", Mode: "appointment_only", OpenTime: "1", CloseTime: "2"},
		}},
	})
	want := []waBinary.Node{
		text("email", ""),
		text("description", "Nueva descripción"),
		text("website", "https://lenz.pe"),
		{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": "America/Lima"}, Content: []waBinary.Node{
			{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "mon", "mode": "specific_hours", "open_time": "540", "close_time": "1080"}},
			// Non-specific modes never carry times, whatever the caller passed.
			{Tag: "business_hours_config", Attrs: waBinary.Attrs{"day_of_week": "sun", "mode": "appointment_only"}},
		}},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("got %+v\nwant %+v", nodes, want)
	}
	// Address absent: no node. Websites cleared: one empty element. Week closed: empty hours.
	addr := "x"
	nodes = BuildBusinessProfileUpdateNodes(BusinessProfileUpdate{Address: &addr, Websites: []string{}, Hours: &BusinessHoursUpdate{TimeZone: "UTC", Days: []BusinessHoursDay{}}})
	want = []waBinary.Node{
		text("address", "x"),
		text("website", ""),
		{Tag: "business_hours", Attrs: waBinary.Attrs{"timezone": "UTC"}, Content: []waBinary.Node{}},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("got %+v\nwant %+v", nodes, want)
	}
	if len(BuildBusinessProfileUpdateNodes(BusinessProfileUpdate{})) != 0 || !(BusinessProfileUpdate{}).IsEmpty() {
		t.Fatal("empty update must render nothing")
	}
	// The rendered nodes must be encodable by the binary marshaller.
	if _, err := waBinary.Marshal(waBinary.Node{Tag: "business_profile", Content: nodes}); err != nil {
		t.Fatal(err)
	}
}
