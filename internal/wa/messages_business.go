package wa

import (
	"encoding/json"
	"strings"

	waProto "go.mau.fi/whatsmeow/binary/proto"
)

func extractBusinessText(m *waProto.Message, pm *ParsedMessage) {
	if tmpl := m.GetTemplateMessage(); tmpl != nil {
		if hydrated := hydratedTemplate(tmpl); hydrated != nil {
			if pm.Text == "" {
				var parts []string
				if t := strings.TrimSpace(hydrated.GetHydratedTitleText()); t != "" {
					parts = append(parts, t)
				}
				if b := strings.TrimSpace(hydrated.GetHydratedContentText()); b != "" {
					parts = append(parts, b)
				}
				if f := strings.TrimSpace(hydrated.GetHydratedFooterText()); f != "" {
					parts = append(parts, "["+f+"]")
				}
				pm.Text = strings.Join(parts, "\n")
			}
			for _, hb := range hydrated.GetHydratedButtons() {
				if btn := hb.GetUrlButton(); btn != nil {
					pm.Buttons = append(pm.Buttons, Button{
						Type:        "url",
						DisplayText: strings.TrimSpace(btn.GetDisplayText()),
						URL:         strings.TrimSpace(btn.GetURL()),
					})
				} else if btn := hb.GetQuickReplyButton(); btn != nil {
					pm.Buttons = append(pm.Buttons, Button{
						Type:        "quick_reply",
						DisplayText: strings.TrimSpace(btn.GetDisplayText()),
						ID:          strings.TrimSpace(btn.GetID()),
					})
				} else if btn := hb.GetCallButton(); btn != nil {
					pm.Buttons = append(pm.Buttons, Button{
						Type:        "call",
						DisplayText: strings.TrimSpace(btn.GetDisplayText()),
						PhoneNumber: strings.TrimSpace(btn.GetPhoneNumber()),
					})
				}
			}
		} else if im := tmpl.GetInteractiveMessageTemplate(); im != nil {
			if pm.Text == "" {
				pm.Text = interactiveText(im)
			}
			appendNativeFlowButtons(pm, im)
		}
	}

	if btn := m.GetButtonsMessage(); btn != nil {
		if pm.Text == "" {
			var parts []string
			if t := strings.TrimSpace(btn.GetText()); t != "" {
				parts = append(parts, t)
			}
			if b := strings.TrimSpace(btn.GetContentText()); b != "" {
				parts = append(parts, b)
			}
			if f := strings.TrimSpace(btn.GetFooterText()); f != "" {
				parts = append(parts, "["+f+"]")
			}
			pm.Text = strings.Join(parts, "\n")
		}
		for _, b := range btn.GetButtons() {
			if bt := b.GetButtonText(); bt != nil {
				dt := strings.TrimSpace(bt.GetDisplayText())
				if dt != "" {
					pm.Buttons = append(pm.Buttons, Button{
						Type:        "quick_reply",
						DisplayText: dt,
						ID:          strings.TrimSpace(b.GetButtonID()),
					})
				}
			}
		}
	}

	if resp := m.GetButtonsResponseMessage(); resp != nil && pm.Text == "" {
		pm.Text = resp.GetSelectedDisplayText()
	}

	if im := m.GetInteractiveMessage(); im != nil {
		if pm.Text == "" {
			pm.Text = interactiveText(im)
		}
		appendNativeFlowButtons(pm, im)
	}

	if resp := m.GetInteractiveResponseMessage(); resp != nil && pm.Text == "" {
		if body := resp.GetBody(); body != nil {
			pm.Text = strings.TrimSpace(body.GetText())
		}
	}

	if list := m.GetListMessage(); list != nil {
		if pm.Text == "" {
			var parts []string
			if t := strings.TrimSpace(list.GetTitle()); t != "" {
				parts = append(parts, t)
			}
			if d := strings.TrimSpace(list.GetDescription()); d != "" {
				parts = append(parts, d)
			}
			pm.Text = strings.Join(parts, "\n")
		}
		if bt := strings.TrimSpace(list.GetButtonText()); bt != "" {
			pm.Buttons = append(pm.Buttons, Button{Type: "list", DisplayText: bt})
		}
		for _, sec := range list.GetSections() {
			for _, row := range sec.GetRows() {
				dt := strings.TrimSpace(row.GetTitle())
				if dt == "" {
					continue
				}
				pm.Buttons = append(pm.Buttons, Button{
					Type:        "list_row",
					DisplayText: dt,
					ID:          strings.TrimSpace(row.GetRowID()),
					Description: strings.TrimSpace(row.GetDescription()),
				})
			}
		}
	}

	if lr := m.GetListResponseMessage(); lr != nil && pm.Text == "" {
		pm.Text = strings.TrimSpace(lr.GetTitle())
		if pm.Text == "" {
			if sel := lr.GetSingleSelectReply(); sel != nil {
				pm.Text = sel.GetSelectedRowID()
			}
		}
	}

	if tbr := m.GetTemplateButtonReplyMessage(); tbr != nil && pm.Text == "" {
		pm.Text = tbr.GetSelectedDisplayText()
	}
}

func hydratedTemplate(tmpl *waProto.TemplateMessage) *waProto.TemplateMessage_HydratedFourRowTemplate {
	if h := tmpl.GetHydratedFourRowTemplate(); h != nil {
		return h
	}
	return tmpl.GetHydratedTemplate()
}

func appendNativeFlowButtons(pm *ParsedMessage, im *waProto.InteractiveMessage) {
	if nf := im.GetNativeFlowMessage(); nf != nil {
		for _, btn := range nf.GetButtons() {
			pm.Buttons = append(pm.Buttons, nativeFlowButton(btn)...)
		}
	}
}

func nativeFlowButton(btn *waProto.InteractiveMessage_NativeFlowMessage_NativeFlowButton) []Button {
	name := strings.TrimSpace(btn.GetName())
	raw := strings.TrimSpace(btn.GetButtonParamsJSON())
	if raw == "" {
		return nil
	}
	switch name {
	case "cta_url":
		var p struct {
			DisplayText string `json:"display_text"`
			URL         string `json:"url"`
		}
		if json.Unmarshal([]byte(raw), &p) == nil && (p.DisplayText != "" || p.URL != "") {
			return []Button{{Type: "url", DisplayText: strings.TrimSpace(p.DisplayText), URL: strings.TrimSpace(p.URL)}}
		}
	case "quick_reply":
		var p struct {
			DisplayText string `json:"display_text"`
			ID          string `json:"id"`
		}
		if json.Unmarshal([]byte(raw), &p) == nil && p.DisplayText != "" {
			return []Button{{Type: "quick_reply", DisplayText: strings.TrimSpace(p.DisplayText), ID: strings.TrimSpace(p.ID)}}
		}
	case "cta_call":
		var p struct {
			DisplayText string `json:"display_text"`
			PhoneNumber string `json:"phone_number"`
		}
		if json.Unmarshal([]byte(raw), &p) == nil && p.DisplayText != "" {
			return []Button{{Type: "call", DisplayText: strings.TrimSpace(p.DisplayText), PhoneNumber: strings.TrimSpace(p.PhoneNumber)}}
		}
	}
	return nil
}

func interactiveText(im *waProto.InteractiveMessage) string {
	var parts []string
	if h := im.GetHeader(); h != nil {
		if t := strings.TrimSpace(h.GetTitle()); t != "" {
			parts = append(parts, t)
		}
	}
	if b := im.GetBody(); b != nil {
		if t := strings.TrimSpace(b.GetText()); t != "" {
			parts = append(parts, t)
		}
	}
	if f := im.GetFooter(); f != nil {
		if t := strings.TrimSpace(f.GetText()); t != "" {
			parts = append(parts, "["+t+"]")
		}
	}
	return strings.Join(parts, "\n")
}
