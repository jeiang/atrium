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
}

type Thread struct {
	MessageID   string
	InReplyTo   string
	References  string
	GmailThread string
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
	Limit    int
}

type Provider interface {
	Message(ctx context.Context, q db.Querier, id int64) (*Message, error)
	Messages(ctx context.Context, q db.Querier, query Query) ([]Message, error)
	Body(ctx context.Context, q db.Querier, id int64) (string, error)
	Parts(ctx context.Context, q db.Querier, id int64) ([]Part, error)
	Part(ctx context.Context, q db.Querier, id int64, part string, limit int) ([]byte, error)
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
