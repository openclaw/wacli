package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/openclaw/wacli/internal/sqliteutil"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow/types"
)

type readOnlySessionResolver struct {
	db *sql.DB
}

func openReadOnlySessionResolver(path string) (*readOnlySessionResolver, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("session db path is required")
	}
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("session db path must not contain '?' or '#'")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", readOnlySessionURI(path))
	if err != nil {
		return nil, fmt.Errorf("open session sqlite: %w", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open read-only session sqlite: %w", err)
	}
	return &readOnlySessionResolver{db: db}, nil
}

func readOnlySessionURI(path string) string {
	params := "_foreign_keys=on&_busy_timeout=5000&mode=ro&_query_only=1"
	if !sessionSQLiteSidecarsExist(path) {
		params += "&immutable=1"
	}
	return sqliteutil.FileURI(path, params)
}

func sessionSQLiteSidecarsExist(path string) bool {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true
		}
	}
	return false
}

func (r *readOnlySessionResolver) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *readOnlySessionResolver) ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	if ownPN := r.resolveOwnPNForLID(ctx, jid); !ownPN.IsEmpty() {
		return ownPN
	}
	var user string
	if err := r.db.QueryRowContext(ctx, "SELECT pn FROM whatsmeow_lid_map WHERE lid=?", jid.User).Scan(&user); err != nil {
		return jid
	}
	return types.JID{User: user, Device: jid.Device, Server: types.DefaultUserServer}
}

func (r *readOnlySessionResolver) ResolvePNToLID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return jid
	}
	if ownLID := r.resolveOwnLIDForPN(ctx, jid); !ownLID.IsEmpty() {
		return ownLID
	}
	var user string
	if err := r.db.QueryRowContext(ctx, "SELECT lid FROM whatsmeow_lid_map WHERE pn=?", jid.User).Scan(&user); err != nil {
		return jid
	}
	return types.JID{User: user, Device: jid.Device, Server: types.HiddenUserServer}
}

func (r *readOnlySessionResolver) resolveOwnLIDForPN(ctx context.Context, jid types.JID) types.JID {
	rows, err := r.db.QueryContext(ctx, "SELECT jid, lid FROM whatsmeow_device")
	if err != nil {
		return types.EmptyJID
	}
	defer rows.Close()
	for rows.Next() {
		var pnText, lidText string
		if err := rows.Scan(&pnText, &lidText); err != nil {
			return types.EmptyJID
		}
		pn, err := types.ParseJID(strings.TrimSpace(pnText))
		if err != nil || pn.ToNonAD() != jid.ToNonAD() {
			continue
		}
		lid, err := types.ParseJID(strings.TrimSpace(lidText))
		if err == nil && lid.Server == types.HiddenUserServer {
			return types.JID{User: lid.User, Device: jid.Device, Server: types.HiddenUserServer}
		}
	}
	return types.EmptyJID
}

func (r *readOnlySessionResolver) resolveOwnPNForLID(ctx context.Context, jid types.JID) types.JID {
	rows, err := r.db.QueryContext(ctx, "SELECT jid, lid FROM whatsmeow_device")
	if err != nil {
		return types.EmptyJID
	}
	defer rows.Close()
	for rows.Next() {
		var pnText, lidText string
		if err := rows.Scan(&pnText, &lidText); err != nil {
			return types.EmptyJID
		}
		lid, err := types.ParseJID(strings.TrimSpace(lidText))
		if err != nil || lid.ToNonAD() != jid.ToNonAD() {
			continue
		}
		pn, err := types.ParseJID(strings.TrimSpace(pnText))
		if err == nil && pn.Server == types.DefaultUserServer {
			return types.JID{User: pn.User, Device: jid.Device, Server: types.DefaultUserServer}
		}
	}
	return types.EmptyJID
}

func (r *readOnlySessionResolver) ResolveChatName(ctx context.Context, chat types.JID, pushName string) string {
	if chat.Server == types.GroupServer || chat.Server == types.NewsletterServer || chat.IsBroadcastList() {
		return chatNameFallback(chat, pushName)
	}
	var first, full, push, business, redacted sql.NullString
	err := r.db.QueryRowContext(ctx, `
		SELECT first_name, full_name, push_name, business_name, redacted_phone
		FROM whatsmeow_contacts
		WHERE their_jid=?
		ORDER BY full_name <> '' DESC, first_name <> '' DESC, business_name <> '' DESC, push_name <> '' DESC, redacted_phone <> '' DESC
		LIMIT 1
	`, chat.ToNonAD().String()).Scan(&first, &full, &push, &business, &redacted)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return chatNameFallback(chat, pushName)
	}
	if err == nil {
		info := types.ContactInfo{
			Found:         true,
			FirstName:     first.String,
			FullName:      full.String,
			PushName:      push.String,
			BusinessName:  business.String,
			RedactedPhone: redacted.String,
		}
		if name := wa.BestContactName(info); name != "" {
			return name
		}
	}
	return chatNameFallback(chat, pushName)
}

func chatNameFallback(chat types.JID, pushName string) string {
	if name := strings.TrimSpace(pushName); name != "" && name != "-" {
		return name
	}
	return chat.String()
}
