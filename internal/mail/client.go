package mail

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/safety"
)

const (
	ActionSelectRW  = "IMAP SELECT read-write"
	ActionCopy      = "IMAP COPY"
	ActionStore     = "IMAP STORE"
	ActionExpunge   = "IMAP EXPUNGE"
	ActionCreate    = "IMAP CREATE"
	ActionSubscribe = "IMAP SUBSCRIBE"
	ActionDelete    = "IMAP DELETE"
	ActionAppend    = safety.AppendAction
)

type MailboxInfo struct {
	Name      string
	IMAPName  string
	Flags     []string
	Delimiter string
}

type Status struct {
	UIDValidity   uint32
	UIDNext       uint32
	Messages      uint32
	HighestModSeq *uint64
}

type Attachment struct {
	Kind string
	Name string
}

type Message struct {
	UID          uint32
	Flags        []string
	Labels       []string
	ModSeq       uint64
	Size         int64
	GmMsgID      string
	GmThrID      string
	InternalDate string
	Attachments  []Attachment
	Header       []byte
	Text         []byte
}

type UIDSet struct {
	all  bool
	uids []uint32
}

func UIDs(uids ...uint32) UIDSet { return UIDSet{uids: uids} }

func AllUIDs() UIDSet { return UIDSet{all: true} }

func (s UIDSet) All() bool { return s.all }

func (s UIDSet) List() []uint32 { return s.uids }

func (s UIDSet) String() string {
	if s.all {
		return "1:*"
	}
	parts := make([]string, len(s.uids))
	for i, u := range s.uids {
		parts[i] = strconv.FormatUint(uint64(u), 10)
	}
	return strings.Join(parts, ",")
}

type SearchCriteria struct {
	Since       time.Time
	UID         *UIDSet
	HeaderName  string
	HeaderValue string
	GmailRaw    string
}

type FetchItems struct {
	Labels       bool
	Header       bool
	Body         bool
	ChangedSince uint64
}

type CopyUID struct {
	UIDValidity uint32
	Mapping     map[uint32]uint32
}

type Conn interface {
	Connect(ctx context.Context) error
	Close()
	HasCapability(name string) bool
	ListMailboxes(ctx context.Context) ([]MailboxInfo, error)
	Status(ctx context.Context, imapName string) (Status, error)
	Select(ctx context.Context, imapName string) error
	UIDSearch(ctx context.Context, criteria SearchCriteria) ([]uint32, error)
	UIDFetch(ctx context.Context, uids UIDSet, items FetchItems) ([]Message, error)
	IdleChanges(ctx context.Context, duration time.Duration) (bool, error)
	Noop(ctx context.Context) error

	SelectRW(ctx context.Context, imapName string) error
	Copy(ctx context.Context, uids UIDSet, destIMAPName string) (*CopyUID, error)
	StoreFlags(ctx context.Context, uids UIDSet, add bool, flags ...string) error
	StoreLabels(ctx context.Context, uids UIDSet, add bool, labels ...string) error
	UIDExpunge(ctx context.Context, uids UIDSet) error
	Append(ctx context.Context, imapName string, raw []byte, flags ...string) error
	CreateMailbox(ctx context.Context, imapName string) error
	SubscribeMailbox(ctx context.Context, imapName string) error
	DeleteMailbox(ctx context.Context, imapName string) error
}

type Client interface {
	Conn
	gated()
}

func Gated(conn Conn, env config.Env) Client { return gate{conn, env} }

type gate struct {
	conn Conn
	env  config.Env
}

func (gate) gated() {}

func (g gate) check(ctx context.Context, action, target string) error {
	return safety.WriteGate(ctx, g.env, action, target)
}

func (g gate) Connect(ctx context.Context) error { return g.conn.Connect(ctx) }

func (g gate) Close() { g.conn.Close() }

func (g gate) HasCapability(name string) bool { return g.conn.HasCapability(name) }

func (g gate) ListMailboxes(ctx context.Context) ([]MailboxInfo, error) {
	return g.conn.ListMailboxes(ctx)
}

func (g gate) Status(ctx context.Context, imapName string) (Status, error) {
	return g.conn.Status(ctx, imapName)
}

func (g gate) Select(ctx context.Context, imapName string) error { return g.conn.Select(ctx, imapName) }

func (g gate) UIDSearch(ctx context.Context, criteria SearchCriteria) ([]uint32, error) {
	return g.conn.UIDSearch(ctx, criteria)
}

func (g gate) UIDFetch(ctx context.Context, uids UIDSet, items FetchItems) ([]Message, error) {
	return g.conn.UIDFetch(ctx, uids, items)
}

func (g gate) IdleChanges(ctx context.Context, duration time.Duration) (bool, error) {
	return g.conn.IdleChanges(ctx, duration)
}

func (g gate) Noop(ctx context.Context) error { return g.conn.Noop(ctx) }

func (g gate) SelectRW(ctx context.Context, imapName string) error {
	if err := g.check(ctx, ActionSelectRW, ""); err != nil {
		return err
	}
	return g.conn.SelectRW(ctx, imapName)
}

func (g gate) Copy(ctx context.Context, uids UIDSet, destIMAPName string) (*CopyUID, error) {
	if err := g.check(ctx, ActionCopy, ""); err != nil {
		return nil, err
	}
	return g.conn.Copy(ctx, uids, destIMAPName)
}

func (g gate) StoreFlags(ctx context.Context, uids UIDSet, add bool, flags ...string) error {
	if err := g.check(ctx, ActionStore, ""); err != nil {
		return err
	}
	return g.conn.StoreFlags(ctx, uids, add, flags...)
}

func (g gate) StoreLabels(ctx context.Context, uids UIDSet, add bool, labels ...string) error {
	if err := g.check(ctx, ActionStore, ""); err != nil {
		return err
	}
	return g.conn.StoreLabels(ctx, uids, add, labels...)
}

func (g gate) UIDExpunge(ctx context.Context, uids UIDSet) error {
	if err := g.check(ctx, ActionExpunge, ""); err != nil {
		return err
	}
	return g.conn.UIDExpunge(ctx, uids)
}

func (g gate) Append(ctx context.Context, imapName string, raw []byte, flags ...string) error {
	if err := g.check(ctx, ActionAppend, imapName); err != nil {
		return err
	}
	return g.conn.Append(ctx, imapName, raw, flags...)
}

func (g gate) CreateMailbox(ctx context.Context, imapName string) error {
	if err := g.check(ctx, ActionCreate, ""); err != nil {
		return err
	}
	return g.conn.CreateMailbox(ctx, imapName)
}

func (g gate) SubscribeMailbox(ctx context.Context, imapName string) error {
	if err := g.check(ctx, ActionSubscribe, ""); err != nil {
		return err
	}
	return g.conn.SubscribeMailbox(ctx, imapName)
}

func (g gate) DeleteMailbox(ctx context.Context, imapName string) error {
	if err := g.check(ctx, ActionDelete, ""); err != nil {
		return err
	}
	return g.conn.DeleteMailbox(ctx, imapName)
}
