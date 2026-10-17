package wa

import (
	"testing"

	"go.mau.fi/whatsmeow"
)

// Hooks for the wa_test package, which drives the real App and the real SDK
// client together.

func RawClient(c *Client) *whatsmeow.Client { return c.client }

func PairedSessionStoreAt(t *testing.T, path string) { newPairedSessionStoreAt(t, path) }
