package review

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/proposals"
)

const (
	CategorySettings   = "settings"
	CategoryOperations = "operations"
)

type Category struct {
	Name       string
	Operations []string
}

type Kinds struct {
	Mode    func(context.Context, db.Querier) (string, error)
	Needs   func(context.Context, db.Querier) ([]Need, error)
	Drafts  func(ctx context.Context, take func(int) int) ([]Draft, error)
	Actions *Actions
}

type Contribution struct {
	Module   string
	Category string
	Kinds    Kinds
}

type Need struct {
	ID        int64
	Subject   string
	To        string
	Unknown   bool
	CreatedAt float64
}

type Draft struct {
	ID                 int64
	ThreadID           *int64
	Account            string
	ToAddrs            string
	CcAddrs            string
	Subject            string
	Body               string
	InReplyToMessageID *int64
	Status             string
	Unknown            bool
	Revision           int64
	CreatedAt          float64
	UpdatedAt          float64
	SentAt             *float64
	SMTPResult         *string
	Unexpected         []string
}

type Action struct {
	ID       int64
	TS       float64
	Activity MailActivity
}

type MailActivity struct {
	MessageID        int64   `json:"message_id"`
	Subject          *string `json:"subject"`
	Kind             string  `json:"kind"`
	FromName         *string `json:"from_name"`
	FromAddr         *string `json:"from_addr"`
	Account          string  `json:"account"`
	Src              *string `json:"src"`
	Dest             *string `json:"dest"`
	Status           string  `json:"status"`
	Error            *string `json:"error"`
	Undoable         bool    `json:"undoable"`
	State            string  `json:"state"`
	Blocked          bool    `json:"blocked"`
	UndoBlocked      bool    `json:"undo_blocked"`
	Undone           bool    `json:"undone"`
	Resolved         bool    `json:"resolved"`
	EffectiveOutcome string  `json:"effective_outcome,omitempty"`
}

type Undone struct {
	ID           int64
	UndoEffectID int64
	Undone       bool
	Detail       any
	SyncError    *string
}

type Resolved struct {
	ID      int64
	Outcome string
}

type Actions struct {
	Before  func(ctx context.Context, beforeTS float64, beforeID int64, limit int) ([]Action, error)
	Get     func(ctx context.Context, id int64) (*Action, error)
	Undo    func(ctx context.Context, id int64) (Undone, error)
	Resolve func(ctx context.Context, id int64, outcome string, who proposals.Resolver) (Resolved, error)
	Refused func(error) bool
}
