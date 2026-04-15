# wacli Balanced Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix YAML corruption bug, add time filters + flexible format to `messages export`, add HMAC-SHA256 signatures to webhooks, and add full test coverage for all new and fixed code.

**Architecture:** Six files across three packages. Changes are purely additive: new helper functions, new flags, new struct fields — no existing interfaces broken. TDD order: write failing tests first, then minimal implementation to pass them.

**Tech Stack:** Go 1.21+, `crypto/hmac` + `crypto/sha256` + `encoding/hex` (stdlib), `net/http/httptest` (tests), Cobra (CLI flags), existing `store.DB` and `wa.ParsedMessage` types.

---

## File Map

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/out/obsidian.go` | Modify | Add `yamlQuote`, extract `writeMessages`, add `WritePlainMarkdown` |
| `internal/out/obsidian_test.go` | Create | 5 formatter tests |
| `cmd/wacli/messages.go` | Modify | Add `--after`, `--before`, `--format` flags; wire `--json` in export |
| `internal/app/sync.go` | Modify | Add `WebhookSecret` to `SyncOptions`; compute HMAC in `dispatchHooks` |
| `cmd/wacli/sync.go` | Modify | Add `--webhook-secret` and `--hook-workers` flags |
| `internal/app/sync_test.go` | Modify | Add 4 hook dispatch tests |

---

## Task 1: Write Obsidian Formatter Tests (TDD — red phase)

**Files:**
- Create: `internal/out/obsidian_test.go`

- [ ] **Step 1: Create the test file**

```go
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
```

- [ ] **Step 2: Run tests — expect failures**

```bash
cd /home/ghostwind/wacli-dev && go test ./internal/out/... -v -run "TestWrite"
```

Expected: `WritePlainMarkdown` compile error (not defined yet). YAML tests may pass or fail depending on current behavior. Fix after Task 2.

---

## Task 2: Fix YAML Quoting & Extract writeMessages

**Files:**
- Modify: `internal/out/obsidian.go`

- [ ] **Step 1: Replace the file content**

Full new content of `internal/out/obsidian.go`:

```go
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
```

- [ ] **Step 2: Run tests — expect all passing**

```bash
go test ./internal/out/... -v -run "TestWrite"
```

Expected: all 6 tests PASS.

- [ ] **Step 3: Run full test suite**

```bash
go test ./...
```

Expected: all existing tests still pass. No regressions.

- [ ] **Step 4: Commit**

```bash
git add internal/out/obsidian.go internal/out/obsidian_test.go
git commit -m "fix: correct YAML quoting in obsidian formatter and add plain-md format

- Add yamlQuote helper that escapes backslashes, double-quotes, and newlines
- Extract writeMessages to remove duplication between formatters
- Add WritePlainMarkdown for frontmatter-free output
- Add tests for YAML safety, chronological order, media messages"
```

---

## Task 3: Write Hook Dispatch Tests (TDD — red phase)

**Files:**
- Modify: `internal/app/sync_test.go`

- [ ] **Step 1: Add imports to sync_test.go**

The existing imports block starts with `package app`. Add the new imports after the existing ones. The full updated imports block:

```go
import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/steipete/wacli/internal/wa"
)
```

- [ ] **Step 2: Append the 4 new tests at the end of sync_test.go**

```go
func TestDispatchHooks_Exec(t *testing.T) {
	tmp := t.TempDir()
	outFile := filepath.Join(tmp, "out.json")

	a := newTestApp(t)
	pm := wa.ParsedMessage{
		ID:   "exec-test-id",
		Text: "exec hook payload",
	}
	opts := SyncOptions{
		ExecCommand: "cat > " + outFile,
	}

	a.dispatchHooks(context.Background(), opts, pm)

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("exec hook did not create output file: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("exec hook output is not valid JSON: %v\noutput: %s", err, data)
	}
	if got["ID"] != "exec-test-id" {
		t.Errorf("expected ID=exec-test-id, got %v", got["ID"])
	}
}

func TestDispatchHooks_Webhook(t *testing.T) {
	var (
		gotContentType string
		gotBody        []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newTestApp(t)
	pm := wa.ParsedMessage{ID: "wh-test-id", Text: "webhook payload"}
	opts := SyncOptions{WebhookURL: srv.URL}

	a.dispatchHooks(context.Background(), opts, pm)

	if gotContentType != "application/json" {
		t.Errorf("Content-Type: got %q, want application/json", gotContentType)
	}

	var got map[string]any
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("webhook body is not valid JSON: %v\nbody: %s", err, gotBody)
	}
	if got["ID"] != "wh-test-id" {
		t.Errorf("expected ID=wh-test-id, got %v", got["ID"])
	}
}

func TestDispatchHooks_WebhookHMAC(t *testing.T) {
	var (
		gotSig  string
		gotBody []byte
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Wacli-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newTestApp(t)
	pm := wa.ParsedMessage{ID: "hmac-test-id", Text: "signed payload"}
	opts := SyncOptions{WebhookURL: srv.URL, WebhookSecret: "supersecret"}

	a.dispatchHooks(context.Background(), opts, pm)

	if gotSig == "" {
		t.Fatal("X-Wacli-Signature header not present")
	}

	mac := hmac.New(sha256.New, []byte("supersecret"))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if gotSig != want {
		t.Errorf("HMAC mismatch\ngot:  %s\nwant: %s", gotSig, want)
	}
}

func TestDispatchHooks_WebhookTimeout(t *testing.T) {
	// Server that blocks until the client disconnects.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	// Cancel the context immediately — dispatchHooks must not hang.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	a := newTestApp(t)
	pm := wa.ParsedMessage{ID: "timeout-id"}
	opts := SyncOptions{WebhookURL: srv.URL}

	done := make(chan struct{})
	go func() {
		a.dispatchHooks(ctx, opts, pm)
		close(done)
	}()

	select {
	case <-done:
		// passed — returned without hanging
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchHooks hung on a cancelled context")
	}
}
```

- [ ] **Step 3: Run tests — expect failures for HMAC test**

```bash
go test ./internal/app/... -v -run "TestDispatchHooks"
```

Expected: `TestDispatchHooks_Exec`, `TestDispatchHooks_Webhook`, `TestDispatchHooks_WebhookTimeout` PASS. `TestDispatchHooks_WebhookHMAC` FAIL with "X-Wacli-Signature header not present".

---

## Task 4: Add HMAC Signature to Webhook Dispatch

**Files:**
- Modify: `internal/app/sync.go`

- [ ] **Step 1: Add `WebhookSecret` to `SyncOptions`**

Find the `SyncOptions` struct (around line 22–38 of `internal/app/sync.go`) and add one field:

```go
type SyncOptions struct {
	Mode            SyncMode
	AllowQR         bool
	OnQRCode        func(string)
	AfterConnect    func(context.Context) error
	DownloadMedia   bool
	RefreshContacts bool
	RefreshGroups   bool
	IdleExit        time.Duration
	MaxReconnect    time.Duration
	Verbosity       int
	ExecCommand     string
	WebhookURL      string
	WebhookSecret   string // signs payloads with HMAC-SHA256 when non-empty
}
```

- [ ] **Step 2: Add HMAC imports to `internal/app/sync.go`**

Add to the import block:
```go
"crypto/hmac"
"crypto/sha256"
"encoding/hex"
```

- [ ] **Step 3: Update `dispatchHooks` to compute the signature**

Find the webhook block inside `dispatchHooks` (around line 490–510). Replace the existing webhook section with:

```go
// Webhook Hook
if opts.WebhookURL != "" {
    httpClient := &http.Client{
        Timeout: 15 * time.Second,
    }
    req, err := http.NewRequestWithContext(ctx, "POST", opts.WebhookURL, bytes.NewReader(data))
    if err != nil {
        fmt.Fprintf(os.Stderr, "\nWebhook request error: %v\n", err)
        return
    }
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("User-Agent", "wacli-bridge/"+a.Version())

    if opts.WebhookSecret != "" {
        mac := hmac.New(sha256.New, []byte(opts.WebhookSecret))
        mac.Write(data)
        req.Header.Set("X-Wacli-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
    }

    resp, err := httpClient.Do(req)
    if err != nil {
        fmt.Fprintf(os.Stderr, "\nWebhook post error: %v\n", err)
        return
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        fmt.Fprintf(os.Stderr, "\nWebhook returned status: %s\n", resp.Status)
    }
}
```

- [ ] **Step 4: Run HMAC test — expect green**

```bash
go test ./internal/app/... -v -run "TestDispatchHooks"
```

Expected: all 4 `TestDispatchHooks_*` tests PASS.

- [ ] **Step 5: Run full test suite**

```bash
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/sync.go internal/app/sync_test.go
git commit -m "feat: add HMAC-SHA256 webhook signature support

- Add WebhookSecret field to SyncOptions
- Compute HMAC-SHA256 over JSON payload when secret is set
- Emit X-Wacli-Signature: sha256=<hex> header (GitHub Webhooks convention)
- Add dispatch tests: exec, webhook, HMAC verification, timeout safety"
```

---

## Task 5: Add --after/--before/--format/--json to messages export

**Files:**
- Modify: `cmd/wacli/messages.go`

- [ ] **Step 1: Update `newMessagesExportCmd`**

Replace the entire `newMessagesExportCmd` function with:

```go
func newMessagesExportCmd(flags *rootFlags) *cobra.Command {
	var chat string
	var query string
	var limit int
	var output string
	var format string
	var afterStr string
	var beforeStr string

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export messages to Markdown or JSON",
		RunE: func(cmd *cobra.Command, args []string) error {
			if chat == "" && query == "" {
				return fmt.Errorf("either --chat or --query is required")
			}
			if format != "obsidian" && format != "plain-md" {
				return fmt.Errorf("--format must be obsidian or plain-md, got %q", format)
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			var after *time.Time
			var before *time.Time
			if afterStr != "" {
				t, err := parseTime(afterStr)
				if err != nil {
					return err
				}
				after = &t
			}
			if beforeStr != "" {
				t, err := parseTime(beforeStr)
				if err != nil {
					return err
				}
				before = &t
			}

			var msgs []store.Message
			var chatName string
			var chatJID string

			if query != "" {
				msgs, err = a.DB().SearchMessages(store.SearchMessagesParams{
					Query:   query,
					ChatJID: chat,
					Limit:   limit,
					After:   after,
					Before:  before,
				})
				chatName = "Search: " + query
				chatJID = "search"
			} else {
				msgs, err = a.DB().ListMessages(store.ListMessagesParams{
					ChatJID: chat,
					Limit:   limit,
					After:   after,
					Before:  before,
				})
				chatJID = chat
				if len(msgs) > 0 {
					chatName = msgs[0].ChatName
				}
				if chatName == "" {
					chatName = chat
				}
			}
			if err != nil {
				return err
			}

			// Sort chronologically for export (DB returns newest first)
			for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
				msgs[i], msgs[j] = msgs[j], msgs[i]
			}

			var w io.Writer = os.Stdout
			if output != "" {
				f, err := os.Create(output)
				if err != nil {
					return err
				}
				defer f.Close()
				w = f
			}

			if flags.asJSON {
				return out.WriteJSON(w, map[string]any{
					"messages": msgs,
					"fts":      a.DB().HasFTS(),
				})
			}

			if format == "plain-md" {
				return out.WritePlainMarkdown(w, chatName, msgs)
			}
			return out.WriteObsidianMarkdown(w, chatName, chatJID, msgs)
		},
	}

	cmd.Flags().StringVar(&chat, "chat", "", "chat JID to export")
	cmd.Flags().StringVar(&query, "query", "", "search query to export results")
	cmd.Flags().IntVar(&limit, "limit", 1000, "limit number of messages")
	cmd.Flags().StringVar(&output, "output", "", "output file path (default: stdout)")
	cmd.Flags().StringVar(&format, "format", "obsidian", "output format: obsidian|plain-md (ignored when --json is set)")
	cmd.Flags().StringVar(&afterStr, "after", "", "only messages after time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&beforeStr, "before", "", "only messages before time (RFC3339 or YYYY-MM-DD)")
	return cmd
}
```

- [ ] **Step 2: Build to verify no compile errors**

```bash
go build ./cmd/wacli/...
```

Expected: no errors.

- [ ] **Step 3: Smoke test the new flags**

```bash
./wacli messages export --help
```

Expected output includes `--after`, `--before`, `--format`, `--json` (global).

- [ ] **Step 4: Run full test suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wacli/messages.go
git commit -m "feat: add --after/--before/--format/--json to messages export

- --after and --before filter messages by time (mirrors messages list)
- --format obsidian|plain-md selects Markdown variant (default: obsidian)
- --json (global flag) outputs {messages, fts} envelope matching messages list
- Validate --format value and return clear error for unknown formats"
```

---

## Task 6: Add --webhook-secret and --hook-workers to sync

**Files:**
- Modify: `cmd/wacli/sync.go`

- [ ] **Step 1: Update `newSyncCmd`**

Replace the variable declarations block and the `StartHookWorkers` call and `SyncOptions` literal. Full updated function:

```go
func newSyncCmd(flags *rootFlags) *cobra.Command {
	var once bool
	var follow bool
	var idleExit time.Duration
	var maxReconnect time.Duration
	var downloadMedia bool
	var refreshContacts bool
	var refreshGroups bool
	var execCommand string
	var webhookURL string
	var webhookSecret string
	var hookWorkers int

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync messages (requires prior auth; never shows QR)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signalContext()
			defer stop()

			a, lk, err := newApp(ctx, flags, true, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			if err := a.EnsureAuthed(); err != nil {
				return err
			}

			if execCommand != "" || webhookURL != "" {
				a.StartHookWorkers(ctx, hookWorkers)
			}

			mode := appPkg.SyncModeFollow
			if once {
				mode = appPkg.SyncModeOnce
			} else if follow {
				mode = appPkg.SyncModeFollow
			} else {
				mode = appPkg.SyncModeOnce
			}

			res, err := a.Sync(ctx, appPkg.SyncOptions{
				Mode:            mode,
				AllowQR:         false,
				DownloadMedia:   downloadMedia,
				RefreshContacts: refreshContacts,
				RefreshGroups:   refreshGroups,
				IdleExit:        idleExit,
				MaxReconnect:    maxReconnect,
				ExecCommand:     execCommand,
				WebhookURL:      webhookURL,
				WebhookSecret:   webhookSecret,
			})
			if err != nil {
				return err
			}

			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]any{
					"synced":          true,
					"messages_stored": res.MessagesStored,
				})
			}
			fmt.Fprintf(os.Stdout, "Messages stored: %d\n", res.MessagesStored)
			return nil
		},
	}

	cmd.Flags().BoolVar(&once, "once", false, "sync until idle and exit")
	cmd.Flags().BoolVar(&follow, "follow", true, "keep syncing until Ctrl+C")
	cmd.Flags().DurationVar(&idleExit, "idle-exit", 30*time.Second, "exit after being idle (once mode)")
	cmd.Flags().DurationVar(&maxReconnect, "max-reconnect", 5*time.Minute, "give up reconnecting after this duration (0 = unlimited)")
	cmd.Flags().BoolVar(&downloadMedia, "download-media", false, "download media in the background during sync")
	cmd.Flags().BoolVar(&refreshContacts, "refresh-contacts", false, "refresh contacts from session store into local DB")
	cmd.Flags().BoolVar(&refreshGroups, "refresh-groups", false, "refresh joined groups (live) into local DB")
	cmd.Flags().StringVar(&execCommand, "exec", "", "command to execute on new message (JSON passed via STDIN)")
	cmd.Flags().StringVar(&webhookURL, "webhook", "", "URL to POST new message JSON")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "HMAC-SHA256 secret for X-Wacli-Signature header")
	cmd.Flags().IntVar(&hookWorkers, "hook-workers", 4, "number of parallel workers for hook dispatch")
	return cmd
}
```

- [ ] **Step 2: Build to verify**

```bash
go build ./cmd/wacli/...
```

Expected: no errors.

- [ ] **Step 3: Verify flags appear in help**

```bash
./wacli sync --help
```

Expected: `--webhook-secret` and `--hook-workers` appear in flags list.

- [ ] **Step 4: Run full test suite**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wacli/sync.go
git commit -m "feat: add --webhook-secret and --hook-workers flags to sync

- --webhook-secret passes HMAC secret to SyncOptions.WebhookSecret
- --hook-workers configures worker pool size (default: 4)
- Both flags no-op when --exec and --webhook are not set"
```

---

## Self-Review Checklist

After completing all tasks, verify the spec is fully covered:

- [ ] **1.1 YAML bug** → Fixed in Task 2 (`yamlQuote`). Test: `TestWriteObsidianMarkdown_YAMLFrontmatter` + `TestWriteObsidianMarkdown_NewlineInChatName`.
- [ ] **1.2 --after/--before on export** → Task 5. Verified via `--help` smoke test.
- [ ] **1.3 --hook-workers configurable** → Task 6. Default 4 preserved.
- [ ] **2.1 HMAC webhook** → Task 4 (SyncOptions field + dispatchHooks) + Task 6 (flag wiring). Test: `TestDispatchHooks_WebhookHMAC`.
- [ ] **2.2 --format + --json on export** → Task 5. Both modes exercised, format validated.
- [ ] **Tests 3.1** → Task 1+2: 6 tests in `obsidian_test.go`.
- [ ] **Tests 3.2** → Task 3+4: 4 tests in `sync_test.go`.

Run final check:

```bash
go test ./... && echo "ALL PASS"
```
