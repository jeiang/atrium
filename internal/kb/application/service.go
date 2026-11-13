package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

type Service struct {
	db    *db.DB
	state *state.Store
	now   func() float64
}

func New(d *db.DB, now func() float64) *Service {
	return &Service{db: d, state: state.New(d), now: now}
}

type Mark struct {
	Name  string
	Label string
	Mark  tool.Marks
}

var Marks = []Mark{
	{"untrusted", "Untrusted", tool.Untrusted},
	{"pii", "PII", tool.PII},
	{"messages", "Messages", tool.Messages},
	{"personal", "Personal", tool.Personal},
}

func MarkNames(marks tool.Marks) []string {
	out := []string{}
	for _, m := range Marks {
		if marks&m.Mark != 0 {
			out = append(out, m.Name)
		}
	}
	return out
}

func (s *Service) snapshot(ctx context.Context, projection string, scopes []string, input any, fn func(db.Querier) error) (application.Revision, error) {
	tx, err := s.db.Read.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	counters, err := s.state.Read(ctx, tx, scopes)
	if err != nil {
		return "", err
	}
	if err := fn(tx); err != nil {
		return "", err
	}
	return application.Revision(state.Revision(projection, counters, input, "operator")), nil
}

func invalid(field, message string) error {
	return &application.Error{Code: application.Invalid, Message: message,
		Fields: []application.Field{{Name: field, Code: "invalid", Message: message}}}
}

func notFound(message string) error {
	return &application.Error{Code: application.NotFound, Message: message}
}
