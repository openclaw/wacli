package out

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/steipete/wacli/internal/store"
)

// yamlQuote returns s safely wrapped in YAML double-quoted scalar syntax.
// Newlines become spaces; backslashes and double-quotes are escaped.
func yamlQuote(s string) string {
	s = strings.NewReplacer(
		"\n", " ",
		"\r", " ",
		`\`, `\\`,
		`"`, `\"`,
	).Replace(s)
	return `"` + s + `"`
}

// writeMessages writes the dated message list shared by both formatters.
func writeMessages(w io.Writer, messages []store.Message) {
	var lastDate string
	for _, m := range messages {
		date := m.Timestamp.Local().Format("2006-01-02")
		if date != lastDate {
			fmt.Fprintf(w, "## %s\n", date)
			lastDate = date
		}

		from := m.SenderJID
		if m.FromMe {
			from = "me"
		}

		text := strings.TrimSpace(m.DisplayText)
		if text == "" {
			text = strings.TrimSpace(m.Text)
		}
		if m.MediaType != "" && text == "" {
			text = "_Sent " + m.MediaType + "_"
		}

		fmt.Fprintf(w, "- **%s** (%s): %s\n",
			from,
			m.Timestamp.Local().Format("15:04:05"),
			text,
		)
	}
}

// WriteObsidianMarkdown writes messages in Obsidian-flavored Markdown with YAML frontmatter.
func WriteObsidianMarkdown(w io.Writer, chatName string, chatJID string, messages []store.Message) error {
	exportDate := time.Now().Format("2006-01-02")
	if len(messages) > 0 {
		exportDate = messages[len(messages)-1].Timestamp.Local().Format("2006-01-02")
	}

	fmt.Fprintln(w, "---")
	fmt.Fprintf(w, "projeto: %s\n", yamlQuote(chatName))
	fmt.Fprintln(w, "tipo: WhatsApp Export")
	fmt.Fprintln(w, "agente: wacli-bridge")
	fmt.Fprintf(w, "data: %s\n", exportDate)
	fmt.Fprintln(w, "status: archived")
	fmt.Fprintf(w, "tags: [whatsapp, export, %s]\n", yamlQuote(strings.ReplaceAll(chatJID, "@", "_")))
	fmt.Fprintln(w, "---")
	fmt.Fprintln(w)

	fmt.Fprintf(w, "# %s\n\n", chatName)
	writeMessages(w, messages)
	return nil
}

// WritePlainMarkdown writes messages as plain Markdown without YAML frontmatter.
func WritePlainMarkdown(w io.Writer, chatName string, messages []store.Message) error {
	fmt.Fprintf(w, "# %s\n\n", chatName)
	writeMessages(w, messages)
	return nil
}
