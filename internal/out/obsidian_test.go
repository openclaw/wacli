package out

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/steipete/wacli/internal/store"
)

func makeMsg(text, mediaType string, ts time.Time, fromMe bool) store.Message {
	return store.Message{
		Text:      text,
		MediaType: mediaType,
		Timestamp: ts,
		FromMe:    fromMe,
		SenderJID: "123@s.whatsapp.net",
	}
}

func TestWriteObsidianMarkdown_YAMLFrontmatter(t *testing.T) {
	var buf bytes.Buffer
	msgs := []store.Message{
		makeMsg("hello", "", time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), false),
	}

	if err := WriteObsidianMarkdown(&buf, `Chat "with" quotes`, "123@g.us", msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()

	// Must open and close the YAML block
	if !strings.Contains(out, "---\n") {
		t.Error("missing YAML frontmatter delimiter ---")
	}

	// Must contain required keys
	for _, key := range []string{"projeto:", "tipo:", "agente:", "data:", "status:", "tags:"} {
		if !strings.Contains(out, key) {
			t.Errorf("missing YAML key %q", key)
		}
	}

	// Unescaped double-quotes in a YAML double-quoted string are invalid
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "projeto:") {
			// Must NOT contain a bare " after the opening quote
			// valid:   projeto: "Chat \"with\" quotes"
			// invalid: projeto: "Chat "with" quotes"
			inner := strings.TrimPrefix(line, "projeto: ")
			inner = strings.Trim(inner, `"`)
			if strings.Contains(inner, `"`) && !strings.Contains(inner, `\"`) {
				t.Errorf("unescaped quotes in YAML projeto field: %q", line)
			}
		}
	}
}

func TestWriteObsidianMarkdown_NewlineInChatName(t *testing.T) {
	var buf bytes.Buffer
	msgs := []store.Message{
		makeMsg("hello", "", time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), false),
	}

	if err := WriteObsidianMarkdown(&buf, "Chat\nWith\nNewlines", "123@g.us", msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The YAML block must contain exactly two --- delimiters
	out := buf.String()
	parts := strings.SplitN(out, "---\n", 3)
	if len(parts) < 3 {
		t.Errorf("YAML block corrupted by newline in chat name: got %d parts after splitting on ---", len(parts))
	}
}

func TestWriteObsidianMarkdown_ChronologicalOrder(t *testing.T) {
	var buf bytes.Buffer
	base := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	msgs := []store.Message{
		makeMsg("first", "", base, false),
		makeMsg("second", "", base.Add(time.Hour), false),
		makeMsg("third", "", base.Add(2*time.Hour), false),
	}

	if err := WriteObsidianMarkdown(&buf, "Test", "123@g.us", msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	i1, i2, i3 := strings.Index(out, "first"), strings.Index(out, "second"), strings.Index(out, "third")
	if !(i1 < i2 && i2 < i3) {
		t.Errorf("messages out of order: first=%d second=%d third=%d", i1, i2, i3)
	}
}

func TestWriteObsidianMarkdown_MediaMessages(t *testing.T) {
	var buf bytes.Buffer
	msgs := []store.Message{
		makeMsg("", "image", time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), false),
	}

	if err := WriteObsidianMarkdown(&buf, "Test", "123@g.us", msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(buf.String(), "_Sent image_") {
		t.Error("media message should render as '_Sent image_'")
	}
}

func TestWritePlainMarkdown_NoFrontmatter(t *testing.T) {
	var buf bytes.Buffer
	msgs := []store.Message{
		makeMsg("hello", "", time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), false),
	}

	if err := WritePlainMarkdown(&buf, "Test Chat", msgs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "---") {
		t.Error("plain-md must not contain YAML frontmatter (---)")
	}
	if !strings.Contains(out, "# Test Chat") {
		t.Error("expected H1 heading '# Test Chat'")
	}
}

func TestWritePlainMarkdown_SameContent(t *testing.T) {
	var obsidianBuf, plainBuf bytes.Buffer
	base := time.Date(2024, 1, 1, 10, 30, 0, 0, time.UTC)
	msgs := []store.Message{
		makeMsg("hello world", "", base, true),
		makeMsg("", "video", base.Add(time.Minute), false),
	}

	_ = WriteObsidianMarkdown(&obsidianBuf, "Chat", "123@g.us", msgs)
	_ = WritePlainMarkdown(&plainBuf, "Chat", msgs)

	// Strip the YAML frontmatter from the obsidian output
	obsidianBody := obsidianBuf.String()
	parts := strings.SplitN(obsidianBody, "---\n", 3)
	if len(parts) == 3 {
		obsidianBody = parts[2]
	}

	if strings.TrimSpace(obsidianBody) != strings.TrimSpace(plainBuf.String()) {
		t.Errorf("body content mismatch:\nobsidian (after stripping frontmatter):\n%s\nplain:\n%s",
			strings.TrimSpace(obsidianBody), strings.TrimSpace(plainBuf.String()))
	}
}
