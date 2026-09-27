package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/types"
)

// Storing a group message asked WhatsApp's servers for the group's info
// twice, once for the chat name and once to refresh the stored group, and
// rewrote every participant each time, so a history chunk of a few thousand
// group messages spent minutes on round trips. The answer is now kept for a
// while and stored once.
const (
	// groupInfoReuse is how long a group's info is reused. A change to the
	// group that this device is told about ends it early.
	groupInfoReuse = 10 * time.Minute
	// groupInfoFailureReuse is how long a failed lookup is reused: a group
	// this account has left is not asked about for every message it has in
	// the history, and a passing failure clears quickly.
	groupInfoFailureReuse = time.Minute
)

type groupInfoAnswer struct {
	info    *types.GroupInfo
	err     error
	expires time.Time
}

type groupInfoCache struct {
	mu      sync.Mutex
	answers map[types.JID]groupInfoAnswer
	// changes counts forgotten groups, so an answer asked for before a
	// change is not kept after it.
	changes uint64
}

// cachedGroupInfo returns the group's info, asking the servers only when no
// recent answer is kept. asked reports whether this call asked, so the caller
// stores each answer once instead of with every message. info is nil whenever
// err is set.
func (a *App) cachedGroupInfo(ctx context.Context, jid types.JID) (info *types.GroupInfo, asked bool, err error) {
	c := &a.groupInfo
	c.mu.Lock()
	if ans, ok := c.answers[jid]; ok && nowUTC().Before(ans.expires) {
		c.mu.Unlock()
		return ans.info, false, ans.err
	}
	changes := c.changes
	c.mu.Unlock()

	info, err = a.wa.GetGroupInfo(ctx, jid)
	if err != nil {
		info = nil
	}
	if ctx.Err() != nil {
		// The sync is stopping: keep nothing, the next run asks again.
		return info, true, err
	}
	reuse := groupInfoReuse
	if info == nil {
		reuse = groupInfoFailureReuse
	}
	c.mu.Lock()
	if c.changes == changes {
		if c.answers == nil {
			c.answers = make(map[types.JID]groupInfoAnswer)
		}
		c.answers[jid] = groupInfoAnswer{info: info, err: err, expires: nowUTC().Add(reuse)}
	}
	c.mu.Unlock()
	return info, true, err
}

// forgetGroupInfo drops the answer kept for a group that changed, so its next
// message asks again.
func (a *App) forgetGroupInfo(jid types.JID) {
	c := &a.groupInfo
	c.mu.Lock()
	delete(c.answers, jid)
	c.changes++
	c.mu.Unlock()
}

// groupChatName names a group chat from its info the way ResolveChatName
// does, without asking the servers again.
func groupChatName(chat types.JID, info *types.GroupInfo, pushName string) string {
	if info != nil {
		if name := strings.TrimSpace(info.GroupName.Name); name != "" {
			return name
		}
	}
	if name := strings.TrimSpace(pushName); name != "" && name != "-" {
		return name
	}
	return chat.String()
}
