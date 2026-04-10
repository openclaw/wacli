package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/steipete/wacli/internal/store"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

var (
	errNoLocalAnchor = errors.New("no local anchor")
	errBackfillWait  = errors.New("timed out waiting for on-demand history sync response")
)

type BackfillOptions struct {
	ChatJID        string
	Count          int
	Requests       int
	WaitPerRequest time.Duration
	IdleExit       time.Duration
}

type BackfillResult struct {
	ChatJID        string
	RequestsSent   int
	ResponsesSeen  int
	MessagesAdded  int64
	MessagesSynced int64
}

type BackfillFillOptions struct {
	ChatJIDs        []string
	Query           string
	Kind            string
	LimitChats      int
	RequestsPerChat int
	Count           int
	WaitPerRequest  time.Duration
	IdleExit        time.Duration
	RetryStalled    bool
	IncludeBlocked  bool
	OnlyActionable  bool
	ResumeOnly      bool
	ResetInProgress bool
}

type BackfillFillChatResult struct {
	ChatJID       string `json:"chat_jid"`
	Name          string `json:"name,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Status        string `json:"status"`
	BlockedReason string `json:"blocked_reason,omitempty"`
	RequestsSent  int    `json:"requests_sent"`
	ResponsesSeen int    `json:"responses_seen"`
	MessagesAdded int64  `json:"messages_added"`
	ReachedStart  bool   `json:"reached_start"`
	LastError     string `json:"last_error,omitempty"`
}

type BackfillFillResult struct {
	Selected       int                      `json:"selected"`
	Attempted      int                      `json:"attempted"`
	Blocked        int                      `json:"blocked"`
	Completed      int                      `json:"completed"`
	Stalled        int                      `json:"stalled"`
	MessagesAdded  int64                    `json:"messages_added"`
	Chats          []BackfillFillChatResult `json:"chats"`
	Coverage       []store.ChatCoverage     `json:"coverage,omitempty"`
	MessagesSynced int64                    `json:"messages_synced,omitempty"`
}

type onDemandResponse struct {
	chatJID       string
	conversations int
	messages      int
	endType       waHistorySync.Conversation_EndOfHistoryTransferType
}

type backfillAttempt struct {
	response      onDemandResponse
	progressed    bool
	messagesAdded int64
	reachedStart  bool
}

type backfillSession struct {
	a *App

	mu      sync.Mutex
	waitFor string
	waitCh  chan onDemandResponse
}

func (a *App) BackfillHistory(ctx context.Context, opts BackfillOptions) (BackfillResult, error) {
	chatStr := strings.TrimSpace(opts.ChatJID)
	if chatStr == "" {
		return BackfillResult{}, fmt.Errorf("--chat is required")
	}
	chat, err := types.ParseJID(chatStr)
	if err != nil {
		return BackfillResult{}, fmt.Errorf("parse chat JID: %w", err)
	}
	chatStr = chat.String()

	if opts.Count <= 0 {
		opts.Count = 50
	}
	if opts.Requests <= 0 {
		opts.Requests = 1
	}
	if opts.WaitPerRequest <= 0 {
		opts.WaitPerRequest = 60 * time.Second
	}
	if opts.IdleExit <= 0 {
		opts.IdleExit = 5 * time.Second
	}

	if err := a.EnsureAuthed(); err != nil {
		return BackfillResult{}, err
	}

	beforeCount, _ := a.db.CountMessages()
	session := &backfillSession{a: a}
	var chatResult BackfillFillChatResult
	var syncRes SyncResult

	handlerID := a.wa.AddEventHandler(session.handleEvent)
	defer a.wa.RemoveEventHandler(handlerID)

	syncRes, err = a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: opts.IdleExit,
		AfterConnect: func(ctx context.Context) error {
			coverage, err := a.lookupCoverage(chatStr)
			if err != nil {
				return err
			}
			chatResult, err = a.backfillChat(ctx, session, coverage, BackfillFillOptions{
				RequestsPerChat: opts.Requests,
				Count:           opts.Count,
				WaitPerRequest:  opts.WaitPerRequest,
			})
			if err != nil {
				return err
			}
			if chatResult.Status == store.BackfillStatusBlocked && chatResult.BlockedReason == store.BackfillBlockedNoLocalAnchor {
				return fmt.Errorf("no messages for %s in local DB; run `wacli sync` first", chatStr)
			}
			if chatResult.Status == store.BackfillStatusStalled && chatResult.LastError != "" {
				return fmt.Errorf("backfill stopped for %s: %s", chatStr, chatResult.LastError)
			}
			return nil
		},
	})
	if err != nil {
		return BackfillResult{}, err
	}

	afterCount, _ := a.db.CountMessages()
	return BackfillResult{
		ChatJID:        chatStr,
		RequestsSent:   chatResult.RequestsSent,
		ResponsesSeen:  chatResult.ResponsesSeen,
		MessagesAdded:  afterCount - beforeCount,
		MessagesSynced: syncRes.MessagesStored,
	}, nil
}

func (a *App) ListBackfillCoverage(opts BackfillFillOptions) ([]store.ChatCoverage, error) {
	params := store.ListChatCoverageParams{
		Query:          opts.Query,
		Kind:           opts.Kind,
		ChatJIDs:       opts.ChatJIDs,
		Limit:          opts.LimitChats,
		IncludeBlocked: opts.IncludeBlocked,
		OnlyActionable: opts.OnlyActionable,
		OnlyTracked:    opts.ResumeOnly,
	}
	return a.db.ListChatCoverage(params)
}

func (a *App) FillHistory(ctx context.Context, opts BackfillFillOptions) (BackfillFillResult, error) {
	if opts.Count <= 0 {
		opts.Count = 50
	}
	if opts.RequestsPerChat <= 0 {
		opts.RequestsPerChat = 3
	}
	if opts.WaitPerRequest <= 0 {
		opts.WaitPerRequest = 60 * time.Second
	}
	if opts.IdleExit <= 0 {
		opts.IdleExit = 5 * time.Second
	}
	if opts.LimitChats <= 0 {
		opts.LimitChats = 1000
	}

	if err := a.EnsureAuthed(); err != nil {
		return BackfillFillResult{}, err
	}
	if opts.ResetInProgress {
		if err := a.db.ResetBackfillInProgress(time.Now().UTC()); err != nil {
			return BackfillFillResult{}, err
		}
	}

	coverage, err := a.ListBackfillCoverage(BackfillFillOptions{
		ChatJIDs:       opts.ChatJIDs,
		Query:          opts.Query,
		Kind:           opts.Kind,
		LimitChats:     opts.LimitChats,
		IncludeBlocked: true,
		ResumeOnly:     opts.ResumeOnly,
	})
	if err != nil {
		return BackfillFillResult{}, err
	}

	selected, precomputed := selectBackfillCandidates(coverage, opts.RetryStalled)
	for _, chat := range precomputed.Chats {
		if chat.Status != store.BackfillStatusBlocked || chat.BlockedReason == "" {
			continue
		}
		state, err := a.loadBackfillState(chat.ChatJID)
		if err != nil {
			return BackfillFillResult{}, err
		}
		state.ChatJID = chat.ChatJID
		state.Status = store.BackfillStatusBlocked
		state.BlockedReason = chat.BlockedReason
		state.LastError = chat.LastError
		state.UpdatedAt = time.Now().UTC()
		if err := a.db.PutBackfillState(state); err != nil {
			return BackfillFillResult{}, err
		}
	}
	result := BackfillFillResult{
		Selected: len(selected),
		Blocked:  precomputed.Blocked,
		Chats:    precomputed.Chats,
	}
	beforeCount, _ := a.db.CountMessages()

	session := &backfillSession{a: a}
	handlerID := a.wa.AddEventHandler(session.handleEvent)
	defer a.wa.RemoveEventHandler(handlerID)

	syncRes, err := a.Sync(ctx, SyncOptions{
		Mode:     SyncModeOnce,
		AllowQR:  false,
		IdleExit: opts.IdleExit,
		AfterConnect: func(ctx context.Context) error {
			for _, candidate := range selected {
				chatResult, err := a.backfillChat(ctx, session, candidate, opts)
				if err != nil {
					return err
				}
				result.Attempted++
				result.MessagesAdded += chatResult.MessagesAdded
				result.Chats = append(result.Chats, chatResult)
				switch chatResult.Status {
				case store.BackfillStatusBlocked:
					result.Blocked++
				case store.BackfillStatusComplete:
					result.Completed++
				case store.BackfillStatusStalled:
					result.Stalled++
				}
			}
			return nil
		},
	})
	if err != nil {
		return BackfillFillResult{}, err
	}
	afterCount, _ := a.db.CountMessages()
	result.MessagesAdded = afterCount - beforeCount
	result.MessagesSynced = syncRes.MessagesStored
	return result, nil
}

func (a *App) PlanFillHistory(opts BackfillFillOptions) (BackfillFillResult, error) {
	if opts.LimitChats <= 0 {
		opts.LimitChats = 1000
	}
	coverage, err := a.ListBackfillCoverage(BackfillFillOptions{
		ChatJIDs:       opts.ChatJIDs,
		Query:          opts.Query,
		Kind:           opts.Kind,
		LimitChats:     opts.LimitChats,
		IncludeBlocked: true,
		ResumeOnly:     opts.ResumeOnly,
	})
	if err != nil {
		return BackfillFillResult{}, err
	}
	selected, result := selectBackfillCandidates(coverage, opts.RetryStalled)
	result.Selected = len(selected)
	result.Coverage = coverage
	return result, nil
}

func selectBackfillCandidates(coverage []store.ChatCoverage, retryStalled bool) ([]store.ChatCoverage, BackfillFillResult) {
	selected := make([]store.ChatCoverage, 0, len(coverage))
	result := BackfillFillResult{Chats: make([]BackfillFillChatResult, 0)}
	for _, c := range coverage {
		switch c.Status {
		case store.BackfillStatusReady, store.BackfillStatusInProgress:
			selected = append(selected, c)
		case store.BackfillStatusStalled:
			if retryStalled {
				selected = append(selected, c)
			}
		case store.BackfillStatusBlocked:
			result.Blocked++
			result.Chats = append(result.Chats, BackfillFillChatResult{
				ChatJID:       c.ChatJID,
				Name:          c.Name,
				Kind:          c.Kind,
				Status:        c.Status,
				BlockedReason: c.BlockedReason,
				LastError:     c.LastError,
			})
		}
	}
	return selected, result
}

func (a *App) backfillChat(ctx context.Context, session *backfillSession, coverage store.ChatCoverage, opts BackfillFillOptions) (BackfillFillChatResult, error) {
	res := BackfillFillChatResult{
		ChatJID: coverage.ChatJID,
		Name:    coverage.Name,
		Kind:    coverage.Kind,
		Status:  coverage.Status,
	}

	if coverage.MessageCount <= 0 {
		state, _ := a.loadBackfillState(coverage.ChatJID)
		state.ChatJID = coverage.ChatJID
		state.Status = store.BackfillStatusBlocked
		state.BlockedReason = store.BackfillBlockedNoLocalAnchor
		state.LastError = ""
		state.UpdatedAt = time.Now().UTC()
		if err := a.db.PutBackfillState(state); err != nil {
			return res, err
		}
		res.Status = store.BackfillStatusBlocked
		res.BlockedReason = store.BackfillBlockedNoLocalAnchor
		return res, nil
	}

	state, err := a.loadBackfillState(coverage.ChatJID)
	if err != nil {
		return res, err
	}
	state.ChatJID = coverage.ChatJID
	state.Status = store.BackfillStatusInProgress
	state.BlockedReason = ""
	state.LastError = ""
	state.UpdatedAt = time.Now().UTC()
	if err := a.db.PutBackfillState(state); err != nil {
		return res, err
	}

	for i := 0; i < opts.RequestsPerChat; i++ {
		attempt, err := session.requestChat(ctx, coverage.ChatJID, opts.Count, opts.WaitPerRequest)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return res, err
			}
			if errors.Is(err, errNoLocalAnchor) {
				state.Status = store.BackfillStatusBlocked
				state.BlockedReason = store.BackfillBlockedNoLocalAnchor
				state.LastError = ""
				state.UpdatedAt = time.Now().UTC()
				if saveErr := a.db.PutBackfillState(state); saveErr != nil {
					return res, saveErr
				}
				res.Status = state.Status
				res.BlockedReason = state.BlockedReason
				return res, nil
			}
			state.Status = store.BackfillStatusStalled
			state.LastBackfillAt = time.Now().UTC()
			state.LastError = err.Error()
			state.UpdatedAt = state.LastBackfillAt
			if saveErr := a.db.PutBackfillState(state); saveErr != nil {
				return res, saveErr
			}
			res.Status = state.Status
			res.LastError = state.LastError
			return res, nil
		}

		now := time.Now().UTC()
		state.LastBackfillAt = now
		state.RequestsSentTotal++
		res.RequestsSent++
		if attempt.response.messages > 0 || attempt.response.endType != 0 || attempt.response.conversations > 0 {
			state.ResponsesSeenTotal++
			res.ResponsesSeen++
		}

		if attempt.progressed {
			state.LastSuccessAt = now
			state.ConsecutiveNoopRequests = 0
			state.LastError = ""
			res.MessagesAdded += attempt.messagesAdded
		} else {
			state.ConsecutiveNoopRequests++
			if attempt.response.messages <= 0 {
				state.LastError = "no messages returned"
			} else {
				state.LastError = "no older messages were added"
			}
		}

		if attempt.reachedStart {
			state.Status = store.BackfillStatusComplete
			state.ReachedStart = true
			state.BlockedReason = ""
			state.LastError = ""
			state.UpdatedAt = now
			if err := a.db.PutBackfillState(state); err != nil {
				return res, err
			}
			res.Status = state.Status
			res.ReachedStart = true
			return res, nil
		}

		if !attempt.progressed && state.ConsecutiveNoopRequests >= 2 {
			state.Status = store.BackfillStatusStalled
			state.UpdatedAt = now
			if err := a.db.PutBackfillState(state); err != nil {
				return res, err
			}
			res.Status = state.Status
			res.LastError = state.LastError
			return res, nil
		}

		state.UpdatedAt = now
		if err := a.db.PutBackfillState(state); err != nil {
			return res, err
		}
	}

	state.Status = store.BackfillStatusReady
	state.BlockedReason = ""
	state.UpdatedAt = time.Now().UTC()
	if err := a.db.PutBackfillState(state); err != nil {
		return res, err
	}
	res.Status = state.Status
	res.LastError = state.LastError
	return res, nil
}

func (a *App) loadBackfillState(chatJID string) (store.BackfillState, error) {
	state, err := a.db.GetBackfillState(chatJID)
	if err == nil {
		return state, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.BackfillState{ChatJID: chatJID, Status: store.BackfillStatusReady}, nil
	}
	return store.BackfillState{}, err
}

func (a *App) lookupCoverage(chatJID string) (store.ChatCoverage, error) {
	coverage, err := a.db.GetChatCoverage(chatJID)
	if err == nil {
		return coverage, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return store.ChatCoverage{
			ChatJID:       chatJID,
			Status:        store.BackfillStatusBlocked,
			BlockedReason: store.BackfillBlockedNoLocalAnchor,
		}, nil
	}
	return store.ChatCoverage{}, err
}

func (s *backfillSession) handleEvent(evt interface{}) {
	hs, ok := evt.(*events.HistorySync)
	if !ok || hs == nil || hs.Data == nil {
		return
	}
	if hs.Data.GetSyncType() != waHistorySync.HistorySync_ON_DEMAND {
		return
	}

	s.mu.Lock()
	waitFor := s.waitFor
	ch := s.waitCh
	s.mu.Unlock()
	if ch == nil || waitFor == "" {
		return
	}

	for _, conv := range hs.Data.GetConversations() {
		chatJID := strings.TrimSpace(conv.GetID())
		if chatJID == "" || chatJID != waitFor {
			continue
		}
		resp := onDemandResponse{
			chatJID:       chatJID,
			conversations: len(hs.Data.GetConversations()),
			messages:      len(conv.GetMessages()),
			endType:       conv.GetEndOfHistoryTransferType(),
		}
		select {
		case ch <- resp:
		default:
		}
		return
	}
}

func (s *backfillSession) requestChat(ctx context.Context, chatJID string, count int, wait time.Duration) (backfillAttempt, error) {
	chatStr := strings.TrimSpace(chatJID)
	if chatStr == "" {
		return backfillAttempt{}, fmt.Errorf("chat JID is required")
	}
	chat, err := types.ParseJID(chatStr)
	if err != nil {
		return backfillAttempt{}, fmt.Errorf("parse chat JID: %w", err)
	}

	beforeCount, err := s.a.db.CountMessagesForChat(chatStr)
	if err != nil {
		return backfillAttempt{}, err
	}
	oldest, err := s.a.db.GetOldestMessageInfo(chatStr)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return backfillAttempt{}, errNoLocalAnchor
		}
		return backfillAttempt{}, err
	}

	ch := make(chan onDemandResponse, 1)
	s.mu.Lock()
	s.waitFor = chatStr
	s.waitCh = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.waitCh == ch {
			s.waitFor = ""
			s.waitCh = nil
		}
		s.mu.Unlock()
	}()

	reqInfo := types.MessageInfo{
		MessageSource: types.MessageSource{
			Chat:     chat,
			IsFromMe: oldest.FromMe,
		},
		ID:        types.MessageID(oldest.MsgID),
		Timestamp: oldest.Timestamp,
	}

	fmt.Fprintf(os.Stderr, "Requesting %d older messages for %s...\n", count, chatStr)
	if _, err := s.a.wa.RequestHistorySyncOnDemand(ctx, reqInfo, count); err != nil {
		return backfillAttempt{}, err
	}

	var resp onDemandResponse
	select {
	case <-ctx.Done():
		return backfillAttempt{}, ctx.Err()
	case resp = <-ch:
	case <-time.After(wait):
		return backfillAttempt{}, errBackfillWait
	}

	fmt.Fprintf(os.Stderr, "On-demand history sync for %s: %d conversations, %d messages.\n", chatStr, resp.conversations, resp.messages)

	afterCount, err := s.a.db.CountMessagesForChat(chatStr)
	if err != nil {
		return backfillAttempt{}, err
	}
	newOldest, err := s.a.db.GetOldestMessageInfo(chatStr)
	if err != nil {
		return backfillAttempt{}, err
	}

	progressed := afterCount > beforeCount || newOldest.MsgID != oldest.MsgID || newOldest.Timestamp.Before(oldest.Timestamp)
	if !progressed {
		fmt.Fprintf(os.Stderr, "No older messages were added for %s.\n", chatStr)
	}
	if resp.endType == waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY {
		fmt.Fprintf(os.Stderr, "Reached start of chat history for %s.\n", chatStr)
	}

	return backfillAttempt{
		response:      resp,
		progressed:    progressed,
		messagesAdded: afterCount - beforeCount,
		reachedStart:  resp.endType == waHistorySync.Conversation_COMPLETE_AND_NO_MORE_MESSAGE_REMAIN_ON_PRIMARY,
	}, nil
}
