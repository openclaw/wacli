package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/wa"
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
	stored  bool
	ready   chan struct{}
}

// finish wakes waiters. The cache mutex protects this and every entry field.
func (a *groupInfoAnswer) finish() {
	if a.ready != nil {
		close(a.ready)
		a.ready = nil
	}
}

type groupInfoCache struct {
	mu      sync.Mutex
	answers map[types.JID]*groupInfoAnswer
}

// cachedGroupInfo shares concurrent lookups and discards answers invalidated
// while in flight. Network calls never hold the cache mutex.
func (a *App) cachedGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, bool, error) {
	c := &a.groupInfo
	for {
		c.mu.Lock()
		if ans := c.answers[jid]; ans != nil {
			if ans.ready != nil {
				ready := ans.ready
				c.mu.Unlock()
				select {
				case <-ready:
					continue
				case <-ctx.Done():
					return nil, false, ctx.Err()
				}
			}
			if nowUTC().Before(ans.expires) {
				info, unstored, err := ans.info, ans.info != nil && !ans.stored, ans.err
				c.mu.Unlock()
				return info, unstored, err
			}
		}
		ans := &groupInfoAnswer{ready: make(chan struct{})}
		if c.answers == nil {
			c.answers = make(map[types.JID]*groupInfoAnswer)
		}
		c.answers[jid] = ans
		c.mu.Unlock()

		info, err := a.wa.GetGroupInfo(ctx, jid)
		if err != nil {
			info = nil
		}
		c.mu.Lock()
		if c.answers[jid] != ans {
			ans.finish()
			c.mu.Unlock()
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			continue
		}
		if ctx.Err() != nil {
			delete(c.answers, jid)
			ans.finish()
			c.mu.Unlock()
			return nil, false, ctx.Err()
		}
		reuse := groupInfoReuse
		if info == nil {
			reuse = groupInfoFailureReuse
		}
		ans.info, ans.err, ans.expires = info, err, nowUTC().Add(reuse)
		ans.finish()
		c.mu.Unlock()
		return info, info != nil, err
	}
}

// storeGroupChat orders both name and snapshot persistence with invalidation.
// Failed snapshot writes leave the current answer eligible for another attempt.
func (a *App) storeGroupChat(ctx context.Context, pm wa.ParsedMessage) (string, error) {
	c := &a.groupInfo
	for {
		info, _, _ := a.cachedGroupInfo(ctx, pm.Chat)
		c.mu.Lock()
		ans := c.answers[pm.Chat]
		if ans == nil || ans.ready != nil || ans.info != info || !nowUTC().Before(ans.expires) {
			if ctx.Err() == nil {
				c.mu.Unlock()
				continue
			}
			// Inline history still stores messages during shutdown. Use only a
			// current answer for naming, and leave cancelled lookups uncached.
			info = nil
			if ans != nil && ans.ready == nil && nowUTC().Before(ans.expires) {
				info = ans.info
			}
			ans = nil
		}
		name := groupChatName(pm.Chat, info, pm.PushName)
		err := a.upsertMessageChat(pm, name)
		if err == nil && ans != nil && info != nil && !ans.stored {
			if a.storeGroupInfo(ctx, info) == nil {
				ans.stored = true
			}
		}
		c.mu.Unlock()
		return name, err
	}
}

func (a *App) forgetGroupInfo(jid types.JID) {
	c := &a.groupInfo
	c.mu.Lock()
	if ans := c.answers[jid]; ans != nil {
		ans.finish()
		delete(c.answers, jid)
	}
	c.mu.Unlock()
}

func (a *App) forgetAllGroupInfo() {
	c := &a.groupInfo
	c.mu.Lock()
	for _, ans := range c.answers {
		ans.finish()
	}
	clear(c.answers)
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
