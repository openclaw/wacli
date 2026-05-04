package app

import (
	"context"

	"go.mau.fi/whatsmeow/types"
)

func canonicalJID(jid types.JID) types.JID {
	if jid.Server == types.DefaultUserServer {
		return jid.ToNonAD()
	}
	return jid
}

func canonicalJIDString(jid types.JID) string {
	return canonicalJID(jid).String()
}

func (a *App) canonicalStoreJID(ctx context.Context, jid types.JID) types.JID {
	return canonicalJID(a.wa.ResolveLIDToPN(ctx, jid))
}
