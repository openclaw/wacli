package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/openclaw/wacli/internal/store"
)

func writeMessagesList(dst io.Writer, msgs []store.Message, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "TIME\tCHAT\tFROM\tID\tTEXT")
	for _, m := range msgs {
		chatLabel := m.ChatName
		if chatLabel == "" {
			chatLabel = m.ChatJID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.Timestamp.Local().Format("2006-01-02 15:04:05"),
			tableCell(chatLabel, 24, fullOutput),
			tableCell(messageFrom(m), 18, fullOutput),
			tableCell(m.MsgID, 14, fullOutput),
			tableCell(messageLine(m), 80, fullOutput),
		)
	}
	return w.Flush()
}

func writeMessagesSearch(dst io.Writer, msgs []store.Message, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintf(w, "TIME\tCHAT\tFROM\tID\tMATCH\n")
	for _, m := range msgs {
		chatLabel := m.ChatName
		if chatLabel == "" {
			chatLabel = m.ChatJID
		}
		match := m.Snippet
		if match == "" {
			match = messageLine(m)
		} else if q := quotePrefix(m, quoteCap); q != "" {
			match = q + " " + match
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			m.Timestamp.Local().Format("2006-01-02 15:04:05"),
			tableCell(chatLabel, 24, fullOutput),
			tableCell(messageFrom(m), 18, fullOutput),
			tableCell(m.MsgID, 14, fullOutput),
			tableCell(match, 90, fullOutput),
		)
	}
	return w.Flush()
}

func writeMessagesStarred(dst io.Writer, msgs []store.Message, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "STARRED\tTIME\tCHAT\tFROM\tID\tTEXT")
	for _, m := range msgs {
		chatLabel := m.ChatName
		if chatLabel == "" {
			chatLabel = m.ChatJID
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			m.StarredAt.Local().Format("2006-01-02 15:04:05"),
			m.Timestamp.Local().Format("2006-01-02 15:04:05"),
			tableCell(chatLabel, 24, fullOutput),
			tableCell(messageFrom(m), 18, fullOutput),
			tableCell(m.MsgID, 14, fullOutput),
			tableCell(messageLine(m), 80, fullOutput),
		)
	}
	return w.Flush()
}

func writeMessageShow(dst io.Writer, m store.Message) error {
	fmt.Fprintf(dst, "Chat: %s\n", sanitize(m.ChatJID))
	if m.ChatName != "" {
		fmt.Fprintf(dst, "Chat name: %s\n", sanitize(m.ChatName))
	}
	fmt.Fprintf(dst, "ID: %s\n", sanitize(m.MsgID))
	fmt.Fprintf(dst, "Time: %s\n", m.Timestamp.Local().Format(time.RFC3339))
	fmt.Fprintf(dst, "From: %s\n", sanitize(messageFromDetail(m)))
	if m.QuotedMsgID != "" {
		fmt.Fprintf(dst, "Quoted message: %s\n", sanitize(m.QuotedMsgID))
	}
	if m.QuotedSenderName != "" && m.QuotedSenderJID != "" {
		fmt.Fprintf(dst, "Quoted sender: %s (%s)\n", sanitize(m.QuotedSenderName), sanitize(m.QuotedSenderJID))
	} else if m.QuotedSenderJID != "" {
		fmt.Fprintf(dst, "Quoted sender: %s\n", sanitize(m.QuotedSenderJID))
	}
	if m.MediaType != "" {
		fmt.Fprintf(dst, "Media: %s\n", sanitize(m.MediaType))
	}
	if m.MediaCaption != "" {
		fmt.Fprintf(dst, "Caption: %s\n", sanitize(m.MediaCaption))
	}
	if m.Filename != "" {
		fmt.Fprintf(dst, "Filename: %s\n", sanitize(m.Filename))
	}
	if m.MimeType != "" {
		fmt.Fprintf(dst, "MIME type: %s\n", sanitize(m.MimeType))
	}
	if m.LocalPath != "" {
		fmt.Fprintf(dst, "Downloaded: %s\n", sanitize(m.LocalPath))
		if !m.DownloadedAt.IsZero() {
			fmt.Fprintf(dst, "Downloaded at: %s\n", m.DownloadedAt.Local().Format(time.RFC3339))
		}
	}
	if m.IsForwarded {
		fmt.Fprintln(dst, "Forwarded: yes")
		if m.ForwardingScore > 0 {
			fmt.Fprintf(dst, "Forwarding score: %d\n", m.ForwardingScore)
		}
	}
	if m.Starred {
		fmt.Fprintln(dst, "Starred: yes")
		if !m.StarredAt.IsZero() {
			fmt.Fprintf(dst, "Starred at: %s\n", m.StarredAt.Local().Format(time.RFC3339))
		}
	}
	if m.Revoked {
		fmt.Fprintln(dst, "Deleted: yes")
	}
	if m.DeletedForMe {
		fmt.Fprintln(dst, "Deleted for me: yes")
	}
	if m.DeletedAt != nil {
		fmt.Fprintf(dst, "Deleted at: %s\n", m.DeletedAt.Local().Format(time.RFC3339))
	}
	if m.DeletionReason != "" {
		fmt.Fprintf(dst, "Deletion reason: %s\n", sanitize(m.DeletionReason))
	}
	if m.PayloadPurgedAt != nil {
		fmt.Fprintf(dst, "Payload purged at: %s\n", m.PayloadPurgedAt.Local().Format(time.RFC3339))
	}
	body := withCaption(m, messageText(m))
	if q := quotePrefix(m, 0); q != "" {
		body = q + "\n" + ownLine(m)
	}
	fmt.Fprintf(dst, "\n%s\n", sanitizeBody(body))
	if raw := messageRawText(m); raw != "" {
		fmt.Fprintf(dst, "\nRaw text:\n%s\n", sanitizeBody(raw))
	}
	return nil
}

func writeMessageContext(dst io.Writer, msgs []store.Message, selectedID string, fullOutput bool) error {
	w := newTableWriter(dst)
	fmt.Fprintln(w, "TIME\tFROM\tID\tTEXT")
	for _, m := range msgs {
		line := messageContextLine(m)
		if m.MsgID == selectedID {
			line = ">> " + line
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			m.Timestamp.Local().Format("2006-01-02 15:04:05"),
			tableCell(messageFrom(m), 18, fullOutput),
			tableCell(m.MsgID, 14, fullOutput),
			tableCell(line, 100, fullOutput),
		)
	}
	return w.Flush()
}

func messageFrom(m store.Message) string {
	if m.FromMe {
		return "me"
	}
	if name := strings.TrimSpace(m.SenderName); name != "" {
		return name
	}
	return m.SenderJID
}

func messageFromDetail(m store.Message) string {
	if m.FromMe {
		return "me"
	}
	name := strings.TrimSpace(m.SenderName)
	jid := strings.TrimSpace(m.SenderJID)
	switch {
	case name != "" && jid != "" && name != jid:
		return fmt.Sprintf("%s (%s)", name, jid)
	case name != "":
		return name
	case jid != "":
		return jid
	default:
		return "(unknown)"
	}
}

func messageText(m store.Message) string {
	if m.DeletedForMe {
		return store.DeletedForMeMessageDisplayText
	}
	if m.Revoked {
		return store.DeletedMessageDisplayText
	}
	if text := strings.TrimSpace(m.DisplayText); text != "" {
		return text
	}
	if text := strings.TrimSpace(m.Text); text != "" {
		return text
	}
	if strings.TrimSpace(m.MediaType) != "" {
		return "Sent " + messageMediaLabel(m.MediaType)
	}
	return ""
}

func messageRawText(m store.Message) string {
	raw := strings.TrimSpace(m.Text)
	if raw == "" || raw == messageText(m) {
		return ""
	}
	return raw
}

func messageContextLine(m store.Message) string {
	return messageLine(m)
}

// quoteCap is how much of a quote a table line shows: the reply's own words
// come after it.
const quoteCap = 200

// messageLine is a message's line in the tables: a reply's quote, who wrote
// it and its words in «» (withQuotes), then its own words; a media message's
// caption after its placeholder ("Sent image: …").
func messageLine(m store.Message) string {
	if q := quotePrefix(m, quoteCap); q != "" {
		return q + " " + ownLine(m)
	}
	return withCaption(m, messageText(m))
}

// quotePrefix is a reply's quote as withQuotes filled it, "> Name: «words»",
// its words cut at max runes (0: whole); "" for any other message.
func quotePrefix(m store.Message, max int) string {
	q := strings.TrimSpace(m.QuotedText)
	if q == "" {
		return ""
	}
	if r := []rune(q); max > 0 && len(r) > max {
		q = strings.TrimSpace(string(r[:max-1])) + "…"
	}
	if who := strings.TrimSpace(m.QuotedSenderName); who != "" {
		return "> " + who + ": «" + q + "»"
	}
	return "> «" + q + "»"
}

// ownLine is a message's own line: without a reply's quote, its caption after
// a media placeholder.
func ownLine(m store.Message) string {
	text := messageText(m)
	if _, own, ok := replyParts(m); ok {
		text = own
	}
	return withCaption(m, text)
}

// replyParts splits a reply's display text as sync stores it ("> " + quote +
// "\n" + its own line: a media placeholder, its text, or "(message)").
func replyParts(m store.Message) (quote, own string, ok bool) {
	d := m.DisplayText
	if strings.TrimSpace(m.QuotedMsgID) == "" || m.Revoked || m.DeletedForMe || !strings.HasPrefix(d, "> ") {
		return "", "", false
	}
	own = "(message)"
	if strings.TrimSpace(m.MediaType) != "" {
		own = mediaPlaceholder(m.MediaType)
	} else if t := strings.TrimSpace(m.Text); t != "" {
		own = t
	}
	if strings.HasSuffix(d, "\n"+own) {
		return strings.TrimSpace(d[2 : len(d)-len(own)-1]), own, true
	}
	quote, own, _ = strings.Cut(d[2:], "\n")
	return strings.TrimSpace(quote), own, true
}

// withCaption puts a media message's caption (a document's file name) after
// its placeholder, the line's last: sync stores the placeholder alone.
func withCaption(m store.Message, text string) string {
	if strings.TrimSpace(m.MediaType) == "" {
		return text
	}
	p, said := mediaPlaceholder(m.MediaType), []string{}
	for _, c := range []string{m.MediaCaption, m.Text} {
		if c = strings.TrimSpace(c); c != "" && c != "[Audio]" && c != p {
			said = append(said, c)
			break
		}
	}
	if f := strings.TrimSpace(m.Filename); f != "" && strings.EqualFold(m.MediaType, "document") && (len(said) == 0 || said[0] != f) {
		said = append(said, f)
	}
	if len(said) == 0 || (text != p && !strings.HasSuffix(text, "\n"+p)) {
		return text
	}
	return text + ": " + strings.Join(said, " · ")
}

// mediaPlaceholder is the line sync stores for a media message.
func mediaPlaceholder(mediaType string) string {
	return "Sent " + strings.ReplaceAll(messageMediaLabel(mediaType), "_", " ")
}

func messageMediaLabel(mediaType string) string {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if mt == "" {
		return "message"
	}
	return mt
}
