package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/steipete/wacli/internal/fsutil"
	"github.com/steipete/wacli/internal/store"
	"github.com/steipete/wacli/internal/wa"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
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
	GetContact(ctx context.Context, jid types.JID) (types.ContactInfo, error)
	GetAllContacts(ctx context.Context) (map[types.JID]types.ContactInfo, error)

	GetJoinedGroups(ctx context.Context) ([]*types.GroupInfo, error)
	GetGroupInfo(ctx context.Context, jid types.JID) (*types.GroupInfo, error)
	SetGroupName(ctx context.Context, jid types.JID, name string) error
	UpdateGroupParticipants(ctx context.Context, group types.JID, users []types.JID, action wa.GroupParticipantAction) ([]types.GroupParticipant, error)
	GetGroupInviteLink(ctx context.Context, group types.JID, reset bool) (string, error)
	JoinGroupWithLink(ctx context.Context, code string) (types.JID, error)
	LeaveGroup(ctx context.Context, group types.JID) error

	SendText(ctx context.Context, to types.JID, text string) (types.MessageID, error)
	SendProtoMessage(ctx context.Context, to types.JID, msg *waProto.Message) (types.MessageID, error)
	Upload(ctx context.Context, data []byte, mediaType whatsmeow.MediaType) (whatsmeow.UploadResponse, error)
	DownloadMediaToFile(ctx context.Context, directPath string, encFileHash, fileHash, mediaKey []byte, fileLength uint64, mediaType, mmsType string, targetPath string) (int64, error)

	SendChatPresence(ctx context.Context, jid types.JID, state types.ChatPresence, media types.ChatPresenceMedia) error
	DecryptReaction(ctx context.Context, reaction *events.Message) (*waProto.ReactionMessage, error)
	RequestHistorySyncOnDemand(ctx context.Context, lastKnown types.MessageInfo, count int) (types.MessageID, error)
	Logout(ctx context.Context) error
}

type Options struct {
	StoreDir      string
	Version       string
	JSON          bool
	AllowUnauthed bool
}

type App struct {
	opts Options
	wa   WAClient
	db   *store.DB
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
	if a.wa != nil {
		a.wa.Close()
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
		return nil
	}
	return fmt.Errorf("not authenticated; run `wacli auth`")
}

func (a *App) WA() WAClient        { return a.wa }
func (a *App) DB() *store.DB       { return a.db }
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
