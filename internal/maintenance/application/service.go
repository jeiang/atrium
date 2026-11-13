package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/settings"
)

type Service struct {
	env config.Env
	now func() float64
}

func New(env config.Env, now func() float64) *Service {
	return &Service{env: env, now: now}
}

func (s *Service) clock() float64 {
	if s.now != nil {
		return s.now()
	}
	return float64(time.Now().UnixNano()) / 1e9
}

type Encryption struct {
	Encrypted bool   `json:"encrypted"`
	KeyID     string `json:"key_id,omitempty"`
}

type Table struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

func encryption(key db.Key) Encryption {
	if key.IsZero() {
		return Encryption{}
	}
	return Encryption{Encrypted: true, KeyID: dbkey.ID(key)}
}

func tables(in []db.VirtualTable) []Table {
	out := make([]Table, len(in))
	for i, t := range in {
		out[i] = Table{Kind: t.Kind, Name: t.Name, Rows: t.Rows}
	}
	return out
}

func (s *Service) config() (config.Config, error) {
	return config.Load(s.env)
}

func (s *Service) database() (config.Config, error) {
	cfg, err := s.config()
	if err != nil {
		return cfg, err
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return cfg, fmt.Errorf("database %s: %w", cfg.DBPath, err)
	}
	return cfg, nil
}

func (s *Service) key(ctx context.Context) (db.Key, error) {
	return dbkey.Load(s.env, settings.From(ctx))
}

func (s *Service) settle(ctx context.Context, d *db.DB, name application.Name, target string, details map[string]any, err error) (int64, error) {
	outcome := state.OK
	var classified *application.Error
	err = refused(err)
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		outcome = state.Canceled
	case errors.As(err, &classified) && classified.Code == application.Refused:
		outcome = state.Refused
	default:
		outcome = state.Failed
	}
	id, auditErr := audit(ctx, d, name, outcome, target, details)
	if auditErr != nil {
		return 0, errors.Join(err, auditErr)
	}
	return id, err
}

func audit(ctx context.Context, d *db.DB, name application.Name, outcome state.Outcome, target string, details map[string]any) (int64, error) {
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return 0, err
	}
	if ok, err := hasAudit(ctx, d.Write); err != nil || !ok {
		return 0, err
	}
	return state.New(d).Audit(ctx, nil, state.Entry{Operation: string(name), Target: target, DetailsJSON: string(raw), Outcome: outcome})
}

func hasAudit(ctx context.Context, q db.Querier) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'operation_audit'").Scan(&n)
	return n > 0, err
}
