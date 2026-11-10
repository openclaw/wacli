package store

import (
	"fmt"
	"strings"
)

// MessageContact is one shared contact card of a contact message, with the raw
// vCard WhatsApp sent so the type and waid of each TEL survive.
type MessageContact struct {
	ChatJID     string
	MsgID       string
	Position    int
	DisplayName string
	VCard       string
}

// ReplaceMessageContacts stores the cards of a message in order, replacing the
// ones saved before (an edit or a re-delivery carries the full set again).
// Cards without a vCard are skipped; no cards at all leaves the stored ones.
func (d *DB) ReplaceMessageContacts(chatJID, msgID string, cards []MessageContact) error {
	if d == nil {
		return fmt.Errorf("nil db")
	}
	if strings.TrimSpace(chatJID) == "" || strings.TrimSpace(msgID) == "" {
		return fmt.Errorf("message contacts require chat_jid and msg_id")
	}
	kept := make([]MessageContact, 0, len(cards))
	for _, card := range cards {
		if strings.TrimSpace(card.VCard) != "" {
			kept = append(kept, card)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// A purged message stays purged: its payload is not stored again.
	var purged int
	if err := tx.QueryRow(
		`SELECT count(*) FROM message_payload_purges WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID,
	).Scan(&purged); err != nil {
		return fmt.Errorf("check message purge: %w", err)
	}
	if purged > 0 {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM message_contacts WHERE chat_jid = ? AND msg_id = ?`, chatJID, msgID); err != nil {
		return fmt.Errorf("replace message contacts: %w", err)
	}
	for i, card := range kept {
		if _, err := tx.Exec(
			`INSERT INTO message_contacts(chat_jid, msg_id, position, display_name, vcard) VALUES(?, ?, ?, ?, ?)`,
			chatJID, msgID, i, nullString(card.DisplayName), card.VCard,
		); err != nil {
			return fmt.Errorf("insert message contact: %w", err)
		}
	}
	return tx.Commit()
}

// GetMessageContacts returns the stored cards of a message in order.
func (d *DB) GetMessageContacts(chatJID, msgID string) ([]MessageContact, error) {
	if d == nil {
		return nil, fmt.Errorf("nil db")
	}
	rows, err := d.sql.Query(
		`SELECT position, COALESCE(display_name, ''), vcard FROM message_contacts WHERE chat_jid = ? AND msg_id = ? ORDER BY position`,
		chatJID, msgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageContact
	for rows.Next() {
		card := MessageContact{ChatJID: chatJID, MsgID: msgID}
		if err := rows.Scan(&card.Position, &card.DisplayName, &card.VCard); err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, rows.Err()
}
