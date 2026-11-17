package communications

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/db"
)

var ErrTooLarge = errors.New("communications: part exceeds the size cap")

type Address struct {
	Name string
	Addr string
}

type Flags struct {
	Seen    bool
	Flagged bool
	InInbox bool
	Sent    bool
	Gone    bool
	Draft   bool
}

type Thread struct {
	MessageID   string
	InReplyTo   string
	References  string
	GmailThread string
}

type Mailing struct {
	Domain     string
	ListID     string
	Precedence string
	Category   string
	ESP        string
	ReplyTo    string
}

type Message struct {
	ID      int64
	Account string
	From    Address
	To      []string
	Subject string
	Date    float64
	Flags   Flags
	Thread  Thread
	Mailing Mailing
}

type Envelope struct {
	Found           bool
	To              []string
	Cc              []string
	ListUnsubscribe string
	AutoSubmitted   string
}

type Deferral interface {
	Err() error
	Note(err error) bool
}

type Prefilter struct {
	Received bool
	Ordered  bool
	Subject  []string
}

type Scan struct {
	Kind       string
	Prefilter  Prefilter
	WindowDays int
	FetchLimit int
	Limit      int
	Paid       Deferral
	Eligible   func(Message) (bool, error)
	Judge      func(ctx context.Context, m Message, body string) (found int, err error)
}

type ScanResult struct {
	Scanned int
	Found   int
}

type Part struct {
	ID          string
	ContentType string
	Params      map[string]string
	Filename    string
	Attachment  bool
}

type Query struct {
	Account  string
	IDs      []int64
	Inbox    bool
	Unseen   bool
	Received bool
	Present  bool
	AfterID  int64
	Since    float64
	ByDate   bool
	Limit    int
}

type Provider interface {
	Message(ctx context.Context, q db.Querier, id int64) (*Message, error)
	Messages(ctx context.Context, q db.Querier, query Query) ([]Message, error)
	Body(ctx context.Context, q db.Querier, id int64) (string, error)
	Parts(ctx context.Context, q db.Querier, id int64) ([]Part, error)
	Part(ctx context.Context, q db.Querier, id int64, part string, limit int) ([]byte, error)
	Envelope(ctx context.Context, q db.Querier, id int64) (Envelope, error)
	Asking(ctx context.Context, q db.Querier, id int64) (*bool, error)
	Selves(ctx context.Context, q db.Querier) ([]string, error)
	Scan(ctx context.Context, d *db.DB, now float64, scan Scan) (ScanResult, error)
}

type Feed struct {
	Unit     string
	Provider Provider
}

func (f Feed) Message(ctx context.Context, q db.Querier, id int64) (*Message, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Message(ctx, q, id)
}

func (f Feed) Messages(ctx context.Context, q db.Querier, query Query) ([]Message, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Messages(ctx, q, query)
}

func (f Feed) Body(ctx context.Context, q db.Querier, id int64) (string, error) {
	if f.Provider == nil {
		return "", nil
	}
	return f.Provider.Body(ctx, q, id)
}

func (f Feed) Parts(ctx context.Context, q db.Querier, id int64) ([]Part, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Parts(ctx, q, id)
}

func (f Feed) Part(ctx context.Context, q db.Querier, id int64, part string, limit int) ([]byte, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Part(ctx, q, id, part, limit)
}

func (f Feed) Envelope(ctx context.Context, q db.Querier, id int64) (Envelope, error) {
	if f.Provider == nil {
		return Envelope{}, nil
	}
	return f.Provider.Envelope(ctx, q, id)
}

func (f Feed) Asking(ctx context.Context, q db.Querier, id int64) (*bool, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Asking(ctx, q, id)
}

func (f Feed) Selves(ctx context.Context, q db.Querier) ([]string, error) {
	if f.Provider == nil {
		return nil, nil
	}
	return f.Provider.Selves(ctx, q)
}

func (f Feed) Scan(ctx context.Context, d *db.DB, now float64, scan Scan) (ScanResult, error) {
	if f.Provider == nil {
		return ScanResult{}, nil
	}
	return f.Provider.Scan(ctx, d, now, scan)
}
