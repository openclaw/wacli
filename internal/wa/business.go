package wa

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow"
	waBinary "go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/types"
)

// Business profile over the raw `w:biz` IQ, the same stanza WhatsApp Web (and
// Baileys) use. whatsmeow's own GetBusinessProfile drops the description and
// the websites, and it has no profile writer, so the nodes are built and
// parsed here. Catalogs live in catalog.go (HTTPS, not an IQ).

const (
	// businessProfileQueryVersion is what GetBusinessProfile and WhatsApp Web send.
	businessProfileQueryVersion = "244"
	// businessProfileMutationVersion is the `v` WhatsApp Web uses for delta updates.
	businessProfileMutationVersion = "3"
)

// BusinessHoursDay is one day of the weekly schedule. Days missing from the
// list are closed. OpenTime/CloseTime are minutes from midnight (0..1439) and
// only meaningful when Mode is "specific_hours".
type BusinessHoursDay struct {
	DayOfWeek string `json:"day_of_week"`
	Mode      string `json:"mode"`
	OpenTime  string `json:"open_time,omitempty"`
	CloseTime string `json:"close_time,omitempty"`
}

// BusinessCategory is a WhatsApp business category (read-only on the wire).
type BusinessCategory struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BusinessProfileFull is the complete business profile as WhatsApp Web shows it.
type BusinessProfileFull struct {
	JID                   string             `json:"jid"`
	Description           string             `json:"description"`
	Address               string             `json:"address"`
	Email                 string             `json:"email"`
	Websites              []string           `json:"websites"`
	Categories            []BusinessCategory `json:"categories"`
	BusinessHoursTimeZone string             `json:"business_hours_timezone"`
	BusinessHours         []BusinessHoursDay `json:"business_hours"`
	ProfileOptions        map[string]string  `json:"profile_options"`
}

// BusinessProfileUpdate is a delta: nil pointers leave the field untouched. An
// empty string clears a text field; an empty, non-nil Websites slice removes
// every website; Hours with an empty Days slice closes the whole week.
type BusinessProfileUpdate struct {
	Description *string
	Address     *string
	Email       *string
	Websites    []string
	Hours       *BusinessHoursUpdate
}

// BusinessHoursUpdate replaces the full weekly schedule.
type BusinessHoursUpdate struct {
	TimeZone string
	Days     []BusinessHoursDay
}

// IsEmpty reports whether the update would send nothing.
func (u BusinessProfileUpdate) IsEmpty() bool {
	return u.Description == nil && u.Address == nil && u.Email == nil && u.Websites == nil && u.Hours == nil
}

var errBusinessProfileMissing = errors.New("business profile missing from response")

func (c *Client) connectedClient() (*whatsmeow.Client, error) {
	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()
	if cli == nil || !cli.IsConnected() {
		return nil, fmt.Errorf("not connected")
	}
	return cli, nil
}

// GetBusinessProfileFull fetches a business profile including its description
// and websites. Pass the account's own JID to read the linked business.
func (c *Client) GetBusinessProfileFull(ctx context.Context, jid types.JID) (*BusinessProfileFull, error) {
	cli, err := c.connectedClient()
	if err != nil {
		return nil, err
	}
	resp, err := cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:biz",
		Type:      "get",
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:   "business_profile",
			Attrs: waBinary.Attrs{"v": businessProfileQueryVersion},
			Content: []waBinary.Node{{
				Tag:   "profile",
				Attrs: waBinary.Attrs{"jid": jid.ToNonAD()},
			}},
		}},
	})
	if err != nil {
		return nil, err
	}
	return ParseBusinessProfileNode(resp)
}

// ParseBusinessProfileNode reads the `business_profile` child of an IQ result.
func ParseBusinessProfileNode(resp *waBinary.Node) (*BusinessProfileFull, error) {
	if resp == nil {
		return nil, errBusinessProfileMissing
	}
	bp, ok := resp.GetOptionalChildByTag("business_profile")
	if !ok {
		return nil, errBusinessProfileMissing
	}
	profile, ok := bp.GetOptionalChildByTag("profile")
	if !ok {
		return nil, errBusinessProfileMissing
	}
	out := &BusinessProfileFull{
		Websites:       []string{},
		Categories:     []BusinessCategory{},
		BusinessHours:  []BusinessHoursDay{},
		ProfileOptions: map[string]string{},
	}
	if jid, ok := profile.AttrGetter().GetJID("jid", false); ok {
		out.JID = jid.String()
	} else if raw, ok := profile.Attrs["jid"].(string); ok {
		out.JID = raw
	}
	for _, child := range profile.GetChildren() {
		switch child.Tag {
		case "description":
			out.Description = nodeText(child)
		case "address":
			out.Address = nodeText(child)
		case "email":
			out.Email = nodeText(child)
		case "website":
			if site := nodeText(child); site != "" {
				out.Websites = append(out.Websites, site)
			}
		case "categories":
			for _, category := range child.GetChildren() {
				if category.Tag != "category" {
					continue
				}
				out.Categories = append(out.Categories, BusinessCategory{
					ID:   category.AttrGetter().OptionalString("id"),
					Name: nodeText(category),
				})
			}
		case "business_hours":
			out.BusinessHoursTimeZone = child.AttrGetter().OptionalString("timezone")
			for _, config := range child.GetChildren() {
				if config.Tag != "business_hours_config" {
					continue
				}
				ag := config.AttrGetter()
				out.BusinessHours = append(out.BusinessHours, BusinessHoursDay{
					DayOfWeek: ag.OptionalString("day_of_week"),
					Mode:      ag.OptionalString("mode"),
					OpenTime:  ag.OptionalString("open_time"),
					CloseTime: ag.OptionalString("close_time"),
				})
			}
		case "profile_options":
			for _, option := range child.GetChildren() {
				out.ProfileOptions[option.Tag] = nodeText(option)
			}
		}
	}
	return out, nil
}

// UpdateBusinessProfile sends a delta mutation of the linked account's business
// profile, the same `w:biz` set WhatsApp Web uses. The caller validates limits.
func (c *Client) UpdateBusinessProfile(ctx context.Context, update BusinessProfileUpdate) error {
	if update.IsEmpty() {
		return fmt.Errorf("business profile update is empty")
	}
	cli, err := c.connectedClient()
	if err != nil {
		return err
	}
	_, err = cli.DangerousInternals().SendIQ(ctx, whatsmeow.DangerousInfoQuery{
		Namespace: "w:biz",
		Type:      "set",
		To:        types.ServerJID,
		Content: []waBinary.Node{{
			Tag:     "business_profile",
			Attrs:   waBinary.Attrs{"v": businessProfileMutationVersion, "mutation_type": "delta"},
			Content: BuildBusinessProfileUpdateNodes(update),
		}},
	})
	return err
}

// BuildBusinessProfileUpdateNodes renders the delta children exactly as
// WhatsApp Web does: text fields as `<tag>text</tag>`, one `<website>` per
// site, and a `<business_hours timezone>` with one config per listed day.
func BuildBusinessProfileUpdateNodes(update BusinessProfileUpdate) []waBinary.Node {
	nodes := []waBinary.Node{}
	text := func(tag string, value *string) {
		if value != nil {
			nodes = append(nodes, waBinary.Node{Tag: tag, Content: []byte(*value)})
		}
	}
	text("address", update.Address)
	text("email", update.Email)
	text("description", update.Description)
	if update.Websites != nil {
		if len(update.Websites) == 0 {
			// Removing every website: WhatsApp Web sends an empty element.
			nodes = append(nodes, waBinary.Node{Tag: "website", Content: []byte{}})
		}
		for _, site := range update.Websites {
			nodes = append(nodes, waBinary.Node{Tag: "website", Content: []byte(site)})
		}
	}
	if update.Hours != nil {
		configs := make([]waBinary.Node, 0, len(update.Hours.Days))
		for _, day := range update.Hours.Days {
			attrs := waBinary.Attrs{"day_of_week": day.DayOfWeek, "mode": day.Mode}
			if day.Mode == "specific_hours" {
				attrs["open_time"] = day.OpenTime
				attrs["close_time"] = day.CloseTime
			}
			configs = append(configs, waBinary.Node{Tag: "business_hours_config", Attrs: attrs})
		}
		nodes = append(nodes, waBinary.Node{
			Tag:     "business_hours",
			Attrs:   waBinary.Attrs{"timezone": update.Hours.TimeZone},
			Content: configs,
		})
	}
	return nodes
}

func nodeText(node waBinary.Node) string {
	switch content := node.Content.(type) {
	case []byte:
		return string(content)
	case string:
		return content
	default:
		return ""
	}
}
