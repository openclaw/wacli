package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/openclaw/wacli/internal/fsutil"
	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/internal/store"
	"github.com/openclaw/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

type WAClient interface {
	Close()
	IsAuthed() bool
	IsConnected() bool
	Connect(ctx context.Context, opts wa.ConnectOptions) error

	AddEventHandler(handler func(interface{})) uint32
	RemoveEventHandler(id uint32)
	ReconnectWithBackoff(ctx context.Context, minDelay, maxDelay time.Duration) error

	ResolveChatName(ctx context.Context, chat types.JID, pushName string) string
	ResolveLIDToPN(ctx context.Context, jid types.JID) types.JID
	ResolvePNToLID(ctx context.Context, jid types.JID) types.JID
	GetUserInfo(ctx context.Context, jids []types.JID) (map[types.JID]types.UserInfo, error)
	IsOnWhatsApp(ctx context.Context, phones []string) ([]types.IsOnWhatsAppResponse, error)
	GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error)
	GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error)

	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error)
	GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
	LeaveGroup(ctx context.Context, group types.JID) error

	GetNewsletterInfoWithInvite(ctx context.Context, key string) (*types.NewsletterMetadata, error)
	FollowNewsletter(ctx context.Context, jid types.JID) error
	UnfollowNewsletter(ctx context.Context, jid types.JID) error
	GetSubscribedNewsletters(ctx context.Context) ([]*types.NewsletterMetadata, error)
	GetNewsletterInfo(ctx context.Context, jid types.JID) (*types.NewsletterMetadata, error)

	SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error)
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
	SendProtoMessageWithExtra(ctx context.Context, to types.JID, msg *waProto.Message, mediaHandle string) (types.MessageID, error)
	SendReaction(ctx context.Context, chat, sender types.JID, targetID types.MessageID, reaction string) (types.MessageID, error)
	SendPoll(ctx context.Context, to types.JID, name string, options []string, selectable int, ephemeral bool) (types.MessageID, error)
	SendPollVote(ctx context.Context, pollInfo *types.MessageInfo, options []string) (types.MessageID, error)
	DecryptPollVote(ctx context.Context, evt *events.Message) (*waE2E.PollVoteMessage, error)
	DecryptSecretEncryptedMessage(ctx context.Context, evt *events.Message) (*waE2E.Message, error)
	RevokeMessage(ctx context.Context, chat types.JID, targetID types.MessageID) (types.MessageID, error)
	DeleteMessageForMe(ctx context.Context, info types.MessageInfo, deleteMedia bool) error
	EditMessage(ctx context.Context, chat types.JID, targetID types.MessageID, text string) (types.MessageID, error)
	ArchiveChat(ctx context.Context, target types.JID, archive bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) error
	PinChat(ctx context.Context, target types.JID, pin bool) error
	MuteChat(ctx context.Context, target types.JID, mute bool, duration time.Duration) error
	MarkChatAsRead(ctx context.Context, target types.JID, read bool, lastMsgTS time.Time, lastMsgKey *waCommon.MessageKey) error
	Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	UploadNewsletter(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error)

	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	ParseWebMessage(chatJID types.JID, webMsg *waWeb.WebMessageInfo) (*events.Message, error)
	DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error)
	SetManualHistorySyncDownload(enabled bool)
	DownloadHistorySync(ctx context.Context, notif *waE2E.HistorySyncNotification) (*waHistorySync.HistorySync, error)
	DeleteHistorySyncMedia(ctx context.Context, notif *waE2E.HistorySyncNotification) error
	RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error)
	FetchAppState(ctx context.Context, name string, fullSync, onlyIfNotSynced bool) error
	RequestAppStateRecovery(ctx context.Context, name string) (types.MessageID, error)
	Logout(ctx context.Context) error
	LinkedJID() string

	SetProfilePicture(ctx context.Context, avatar []byte) (string, error)
}

type Options struct {
	StoreDir      string
	Version       string
	JSON          bool
	Events        *out.EventWriter
	AllowUnauthed bool
}

type App struct {
	opts     Options
	waMu     sync.Mutex
	wa       WAClient
	db       *store.DB
	statusMu sync.Mutex
	status   *syncStatus
}

func New(opts Options) (*App, error) {
	if opts.StoreDir == "" {
		return nil, fmt.Errorf("store dir is required")
	}
	if err := fsutil.EnsurePrivateDir(opts.StoreDir); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	indexPath := filepath.Join(opts.StoreDir, "wacli.db")

	db, err := store.Open(indexPath)
	if err != nil {
		return nil, err
	}

	return &App{opts: opts, db: db}, nil
}

func (a *App) OpenWA() error {
	a.waMu.Lock()
	defer a.waMu.Unlock()
	if a.wa != nil {
		return nil
	}
	sessionPath := filepath.Join(a.opts.StoreDir, "session.db")
	cli, err := wa.New(wa.Options{
		StorePath: sessionPath,
	})
	if err != nil {
		return err
	}

	a.wa = cli
	return nil
}

func (a *App) Close() {
	a.waMu.Lock()
	waClient := a.wa
	a.waMu.Unlock()
	if waClient != nil {
		waClient.Close()
	}
	if a.db != nil {
		_ = a.db.Close()
	}
}

func (a *App) EnsureAuthed() error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	if a.wa.IsAuthed() {
		return a.migrateHistoricalLIDs(context.Background())
	}
	return fmt.Errorf("not authenticated; run `wacli auth`")
}

func (a *App) WA() WAClient {
	a.waMu.Lock()
	defer a.waMu.Unlock()
	return a.wa
}
func (a *App) DB() *store.DB { return a.db }
func (a *App) Events() *out.EventWriter {
	return a.opts.Events
}
func (a *App) StoreDir() string    { return a.opts.StoreDir }
func (a *App) Version() string     { return a.opts.Version }
func (a *App) AllowUnauthed() bool { return a.opts.AllowUnauthed }

func (a *App) Connect(ctx context.Context, allowQR bool, qrWriter func(string)) error {
	if err := a.OpenWA(); err != nil {
		return err
	}
	return a.wa.Connect(ctx, wa.ConnectOptions{
		AllowQR:  allowQR,
		OnQRCode: qrWriter,
	})
}
