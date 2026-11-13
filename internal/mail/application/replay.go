package application

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	dbkey "github.com/jeiang/atrium/internal/db/key"
	"github.com/jeiang/atrium/internal/mail/classify"
	"github.com/jeiang/atrium/internal/mail/replay"
	"github.com/jeiang/atrium/internal/mail/run"
	"github.com/jeiang/atrium/internal/settings"
)

const ReplayName application.Name = "mail.replay"

type ReplayInput struct {
	Account string
	Limit   int
	Model   bool
}

func DescribeReplay() application.Definition {
	return application.Describe[ReplayInput, replay.Result](ReplayName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

type ReplaySourceError struct{ Cause error }

func (e *ReplaySourceError) Error() string { return e.Cause.Error() }
func (e *ReplaySourceError) Unwrap() error { return e.Cause }

func (s *Service) Replay(ctx context.Context, in ReplayInput) (application.Read[replay.Result], error) {
	if in.Limit < 0 {
		return application.Read[replay.Result]{}, &application.Error{Code: application.Invalid, Message: "argument --limit: must be a whole number of at least 0"}
	}
	key, err := dbkey.Load(s.env, settings.From(ctx))
	if err != nil {
		return application.Read[replay.Result]{}, err
	}
	q, err := db.OpenSource(s.cfg.DBPath, key)
	if err != nil {
		return application.Read[replay.Result]{}, &ReplaySourceError{Cause: err}
	}
	defer func() { _ = q.Close() }()
	if err := db.RequireV3(ctx, q); err != nil {
		return application.Read[replay.Result]{}, err
	}
	state := config.From(ctx)
	if err := state.LoadStoredAccounts(ctx, q); err != nil {
		return application.Read[replay.Result]{}, err
	}
	if err := replayAccounts(ctx, state, q); err != nil {
		return application.Read[replay.Result]{}, err
	}
	if in.Account != "" {
		if _, ok := state.Account(in.Account); !ok {
			return application.Read[replay.Result]{}, run.UnknownAccount(in.Account)
		}
	}
	opts := replay.Options{Account: in.Account, Limit: in.Limit, Model: in.Model}
	var result replay.Result
	if in.Model {
		result, err = s.replayModel(ctx, q, key, opts)
	} else {
		result, err = replay.Run(ctx, q, opts, nil)
	}
	return application.Read[replay.Result]{Data: result}, err
}

func (s *Service) replayModel(ctx context.Context, source *sql.DB, key db.Key, opts replay.Options) (_ replay.Result, err error) {
	if s.classifiers == nil {
		return replay.Result{}, errors.New("replay --model: no classifier is bound")
	}
	dir, err := os.MkdirTemp("", "atrium-replay-")
	if err != nil {
		return replay.Result{}, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	path := filepath.Join(dir, "replay.db")
	if err := db.Backup(ctx, source, path, key); err != nil {
		return replay.Result{}, err
	}
	d, err := db.Connect(path, db.WithKey(key))
	if err != nil {
		return replay.Result{}, err
	}
	defer func() { err = errors.Join(err, d.Close()) }()
	ledger, err := db.Connect(s.cfg.DBPath, db.WithKey(key))
	if err != nil {
		return replay.Result{}, err
	}
	defer func() { err = errors.Join(err, ledger.Close()) }()
	c, err := s.classifiers(ctx, s.env, ledger, s.cfg, s.now)
	if err != nil {
		return replay.Result{}, err
	}
	return replay.Run(ctx, d.Read, opts, classify.New(c))
}

func replayAccounts(ctx context.Context, state *config.State, q db.Querier) error {
	rows, err := q.QueryContext(ctx, "SELECT name, kind FROM accounts ORDER BY name")
	if err != nil {
		return err
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (config.AccountSpec, error) {
		var name, kind string
		err := r.Scan(&name, &kind)
		return config.AccountSpec{Name: name, Label: name, Preset: kind, Enabled: true}, err
	})
	if err != nil {
		return err
	}
	accounts := append([]config.AccountSpec(nil), state.Accounts()...)
	seen := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		seen[a.Name] = true
	}
	for _, a := range stored {
		if seen[a.Name] {
			continue
		}
		accounts = append(accounts, a)
		seen[a.Name] = true
	}
	state.UseAccounts(accounts, nil)
	return nil
}
