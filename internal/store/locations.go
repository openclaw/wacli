package store

import (
	"fmt"
	"math"
	"strings"

	"github.com/openclaw/wacli/internal/store/storedb"
)

type MessageLocation struct {
	ChatJID   string
	MsgID     string
	Latitude  float64
	Longitude float64
	Name      string
	Address   string
	IsLive    bool
}

func (d *DB) UpsertMessageLocation(loc MessageLocation) error {
	if d == nil {
		return fmt.Errorf("nil db")
	}
	if strings.TrimSpace(loc.ChatJID) == "" || strings.TrimSpace(loc.MsgID) == "" {
		return fmt.Errorf("message location requires chat_jid and msg_id")
	}
	if err := validateCoordinates(loc.Latitude, loc.Longitude); err != nil {
		return err
	}
	if err := d.q.UpsertMessageLocation(storeCtx(), storedb.UpsertMessageLocationParams{
		ChatJid:   loc.ChatJID,
		MsgID:     loc.MsgID,
		Latitude:  loc.Latitude,
		Longitude: loc.Longitude,
		Name:      nullString(loc.Name),
		Address:   nullString(loc.Address),
		IsLive:    int64(boolToInt(loc.IsLive)),
		ChatJid_2: loc.ChatJID,
		MsgID_2:   loc.MsgID,
	}); err != nil {
		return fmt.Errorf("upsert message location: %w", err)
	}
	return nil
}

func (d *DB) GetMessageLocation(chatJID, msgID string) (MessageLocation, error) {
	if d == nil {
		return MessageLocation{}, fmt.Errorf("nil db")
	}
	row, err := d.q.GetMessageLocation(storeCtx(), storedb.GetMessageLocationParams{
		ChatJid: chatJID,
		MsgID:   msgID,
	})
	if err != nil {
		return MessageLocation{}, err
	}
	return MessageLocation{
		ChatJID:   row.ChatJid,
		MsgID:     row.MsgID,
		Latitude:  row.Latitude,
		Longitude: row.Longitude,
		Name:      row.Name,
		Address:   row.Address,
		IsLive:    row.IsLive != 0,
	}, nil
}

func (d *DB) DeleteMessageLocation(chatJID, msgID string) error {
	if d == nil {
		return fmt.Errorf("nil db")
	}
	if err := d.q.DeleteMessageLocation(storeCtx(), storedb.DeleteMessageLocationParams{
		ChatJid: chatJID,
		MsgID:   msgID,
	}); err != nil {
		return fmt.Errorf("delete message location: %w", err)
	}
	return nil
}

func validateCoordinates(lat, lng float64) error {
	if math.IsNaN(lat) || math.IsNaN(lng) || math.IsInf(lat, 0) || math.IsInf(lng, 0) {
		return fmt.Errorf("message location requires finite coordinates")
	}
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude %v out of range", lat)
	}
	if lng < -180 || lng > 180 {
		return fmt.Errorf("longitude %v out of range", lng)
	}
	return nil
}
