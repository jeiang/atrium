// Package run is run.py and watch.py: the processing pass that follows a sync, the `sync` and `watch` commands and
// the per-account watch loop. Every pass holds the database's exclusive lock.
package run

import (
	"context"
	"fmt"
	"time"

	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/flags"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/router"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.run")

// MissingCredentials is the RuntimeError of an account that has no credentials configured.
type MissingCredentials string

func (e MissingCredentials) Error() string { return "missing credentials for " + string(e) }

// SelectAccounts is select_accounts: the requested account, which must have credentials, or every configured
// account that has them.
func SelectAccounts(cfg config.Config, requested string) ([]string, error) {
	if requested != "" {
		if _, _, ok := cfg.Credential(requested); !ok {
			return nil, MissingCredentials(requested)
		}
		return []string{requested}, nil
	}
	var out []string
	for _, spec := range config.Accounts {
		if _, _, ok := cfg.Credential(spec.Name); ok {
			out = append(out, spec.Name)
		}
	}
	return out, nil
}

// Extra is one refresh after the accounts' passes: its result is logged as "<Name>: <repr>" and a failure as
// "<Name> refresh failed"; neither stops the pass.
type Extra struct {
	Name string
	Run  func(ctx context.Context) (any, error)
}

// Pass is process() over one database. Events is event_candidates.scan_account, run between flagging and the live
// pass when set; Extras run after every account, in order.
type Pass struct {
	DB      *db.DB
	Cfg     config.Config
	Open    mailops.Opener
	Now     func() float64
	Syncer  *mailsync.Syncer
	Live    *live.Live
	Router  *router.Router
	Judge   flags.Judge
	Applier *applier.Applier
	Events  func(ctx context.Context, account string) (invites, candidates int, err error)
	Money   money.Options
	Extras  []Extra
}

// New is the pass run.py makes: the sync, routing, flagging and live pass of the mail core, then the open loops
// and watches refreshes and the money ledger's. now is the clock of every step in seconds, the wall clock when nil.
func New(d *db.DB, cfg config.Config, open mailops.Opener, now func() float64) *Pass {
	if now == nil {
		now = wallClock
	}
	syncer := &mailsync.Syncer{DB: d, Cfg: cfg, Now: now}
	l := &live.Live{DB: d, Cfg: cfg, Now: now}
	p := &Pass{
		DB:      d,
		Cfg:     cfg,
		Open:    open,
		Now:     now,
		Syncer:  syncer,
		Live:    l,
		Router:  &router.Router{DB: d, ChatURL: cfg.ChatURL, Now: now},
		Applier: applier.New(l, open, syncer),
		Events: func(ctx context.Context, account string) (int, int, error) {
			return candidates.ScanAccount(ctx, d, cfg, account, nil, config.OS, now)
		},
	}
	p.Extras = []Extra{
		{"open loops", func(ctx context.Context) (any, error) {
			return openloops.Refresh(ctx, d, cfg, now(), openloops.Options{Clock: now}), nil
		}},
		{"watches", func(ctx context.Context) (any, error) {
			return watches.Refresh(ctx, d, cfg, watches.ScanLimit, now(), watches.Options{Clock: now}), nil
		}},
		{"money", func(ctx context.Context) (any, error) {
			return money.Refresh(ctx, d, cfg, money.ScanLimit, now(), p.Money)
		}},
	}
	return p
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// SyncOne is sync_one: it syncs account on a fresh connection and logs the counts.
func (p *Pass) SyncOne(ctx context.Context, account string) error {
	_, err := p.Syncer.One(ctx, account, mailsync.Opener(p.Open))
	return err
}

// SyncAll is sync_all: every account that has credentials.
func (p *Pass) SyncAll(ctx context.Context) error {
	accounts, err := SelectAccounts(p.Cfg, "")
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if err := p.SyncOne(ctx, account); err != nil {
			return err
		}
	}
	return nil
}

// Process is process(): in live mode the go-live baseline is taken after syncing every account the first time;
// pending messages are embedded (an unavailable embedder is logged); each account is routed, flagged, scanned for
// events and given its live pass; the extras follow. Routing, flagging and embedding errors end the pass; an event
// scan, live pass or extra that fails is logged and the pass goes on. A cancelled ctx is seen between accounts
// and between extras, never inside a live pass, whose writes and resync run to their end.
func (p *Pass) Process(ctx context.Context, accounts []string) error {
	if p.Cfg.Mode == config.Live {
		if _, err := p.Live.EnsureGolive(ctx, func() error { return p.SyncAll(ctx) }); err != nil {
			return err
		}
	}
	if err := embed.PendingOrWarn(ctx, p.DB, p.Cfg.EmbedURL, log); err != nil {
		return err
	}
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		routed, err := p.Router.RouteAccount(ctx, account)
		if err != nil {
			return err
		}
		flagged, err := flags.FlagAccount(ctx, p.DB, p.Cfg, account, p.Judge, p.Now)
		if err != nil {
			return err
		}
		log.Info(fmt.Sprintf("%s: %d routed, %d flag judgments", account, routed, flagged))
		if p.Events != nil {
			if invites, candidates, err := p.Events(ctx, account); err != nil {
				log.Error(account+": event scan failed", logx.Err, err.Error())
			} else {
				log.Info(fmt.Sprintf("%s: %d invitation proposals, %d event candidates", account, invites, candidates))
			}
		}
		if _, _, err := p.Applier.ApplyAll(ctx, account); err != nil {
			log.Error(account+": live pass failed", logx.Err, err.Error())
		}
	}
	for _, extra := range p.Extras {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := extra.Run(ctx)
		if err != nil {
			log.Error(extra.Name+" refresh failed", logx.Err, err.Error())
			continue
		}
		log.Info(extra.Name + ": " + py.ReprValue(result))
	}
	return nil
}

// checkpointWait is how long the end-of-pass checkpoint waits for readers while the lock is held.
const checkpointWait = time.Second

// Locked is one pass of accounts under the database's exclusive lock: each is synced, then processed. The WAL is
// checkpointed (TRUNCATE) before the lock is released, so readers that never stop cannot grow it without bound.
// A ctx cancelled during the pass ends it with its error once the running stage is complete.
func (p *Pass) Locked(ctx context.Context, accounts []string) error {
	lock, err := db.Exclusive(ctx, p.Cfg.DBPath, 0)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	defer p.checkpoint(ctx)
	for _, account := range accounts {
		if err := p.SyncOne(ctx, account); err != nil {
			return err
		}
	}
	if err := p.Process(ctx, accounts); err != nil {
		return err
	}
	return ctx.Err()
}

func (p *Pass) checkpoint(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	c, err := p.DB.Checkpoint(ctx, checkpointWait)
	switch {
	case err != nil:
		log.Warn("wal checkpoint failed", logx.Err, err.Error())
	case c.Busy:
		log.Warn(fmt.Sprintf("wal checkpoint blocked by a reader: %d of %d frames checkpointed", c.Done, c.Frames))
	}
}

// Sync is run_sync: the selected accounts are synced and processed in one locked pass; it returns them.
func Sync(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) ([]string, error) {
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return nil, err
	}
	accounts, err := SelectAccounts(cfg, requested)
	if err != nil {
		return nil, err
	}
	return accounts, New(d, cfg, open, nil).Locked(ctx, accounts)
}
