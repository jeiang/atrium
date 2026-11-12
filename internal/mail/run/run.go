package run

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/flags"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/router"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/proposals"
)

var log = logx.Logger("atrium.run")

type MissingCredentials string

func (e MissingCredentials) Error() string { return "missing credentials for " + string(e) }

type UnknownAccount string

func (e UnknownAccount) Error() string { return "unknown mail account " + string(e) }

func SelectAccounts(ctx context.Context, requested string) ([]string, error) {
	if requested != "" {
		spec, known := config.From(ctx).Account(requested)
		if !known {
			return nil, UnknownAccount(requested)
		}
		if _, _, ok := config.Credential(ctx, requested); !ok && spec.AuthMethod != config.AuthXOAUTH2 {
			return nil, MissingCredentials(requested)
		}
		return []string{requested}, nil
	}
	var out []string
	for _, spec := range config.From(ctx).Accounts() {
		if _, _, ok := config.Credential(ctx, spec.Name); ok || spec.AuthMethod == config.AuthXOAUTH2 {
			out = append(out, spec.Name)
		}
	}
	return out, nil
}

type Extra struct {
	Name string
	Run  func(ctx context.Context) (any, error)
}

type Pass struct {
	DB       *db.DB
	Cfg      config.Config
	Open     mail.OpenFunc
	Now      func() float64
	Syncer   *mailsync.Syncer
	Live     *live.Live
	Registry *proposals.Registry
	Router   *router.Router
	Applier  *applier.Applier
	Events   func(ctx context.Context, account string) (invites, candidates int, err error)
	Extras   []Extra
}

func New(ctx context.Context, env config.Env, d *db.DB, cfg config.Config, open mail.OpenFunc, now func() float64) (*Pass, error) {
	if now == nil {
		now = mail.WallClock
	}
	syncer := &mailsync.Syncer{DB: d, Cfg: cfg, Now: now}
	cat := module.SetFrom(ctx).Catalog()
	store, err := cat.Ledger(d, env, open, now)
	if err != nil {
		return nil, err
	}
	registry, err := cat.Proposals(module.EffectDeps{DB: d, Ledger: store, Open: open, Syncer: syncer, Now: now, Env: env, Approvals: cat.Approvals()})
	if err != nil {
		return nil, err
	}
	l := &live.Live{DB: d, Cfg: cfg, Store: store, Open: open, Registry: registry, Now: now}
	p := &Pass{
		DB:       d,
		Cfg:      cfg,
		Open:     open,
		Now:      now,
		Syncer:   syncer,
		Live:     l,
		Registry: registry,
		Router:   &router.Router{DB: d, Now: now},
		Applier:  applier.New(l, open, syncer),
		Events: func(ctx context.Context, account string) (int, int, error) {
			if !module.IsEnabled(ctx, module.Calendar) {
				return 0, 0, nil
			}
			return candidates.ScanAccount(ctx, d, cfg, account, nil, env, now)
		},
	}
	p.Extras = []Extra{
		{"open loops", func(ctx context.Context) (any, error) {
			return openloops.Refresh(ctx, d, now(), openloops.Options{Env: env, Clock: now, CacheDir: cfg.CacheDir})
		}},
		{"watches", func(ctx context.Context) (any, error) {
			return watches.Refresh(ctx, d, now(), watches.Options{Env: env, Clock: now})
		}},
	}
	return p, nil
}

func Open(ctx context.Context, env config.Env, cfg config.Config, open mail.OpenFunc, now func() float64) (*Pass, func(), error) {
	d, err := db.Connect(cfg.DBPath, db.WithKey(config.From(ctx).DBKey()))
	if err != nil {
		return nil, nil, err
	}
	if err := config.Boot(ctx, env, d, cfg); err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	p, err := New(ctx, env, d, cfg, open, now)
	if err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	return p, func() { _ = d.Close() }, nil
}

func (p *Pass) SyncOne(ctx context.Context, account string) error {
	_, err := p.Syncer.One(ctx, account, p.Open)
	return err
}

func (p *Pass) SyncAll(ctx context.Context) error {
	accounts, err := SelectAccounts(ctx, "")
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

func (p *Pass) Process(ctx context.Context, accounts []string) error {
	mode, err := mail.Mode(ctx, p.DB.Read)
	if err != nil {
		return err
	}
	if mode == config.Live {
		if _, err := p.Live.EnsureGolive(ctx, func() error { return p.SyncAll(ctx) }); err != nil {
			return err
		}
	}
	embedURL, err := embed.EmbedURL(ctx, p.DB.Read)
	if err != nil {
		return err
	}
	ctx, err = embed.WithModels(ctx, p.DB.Read)
	if err != nil {
		return err
	}
	if err := embed.PendingOrWarn(ctx, p.DB, embedURL, log); err != nil {
		return err
	}
	var failed []string
	for _, account := range accounts {
		if err := ctx.Err(); err != nil {
			return err
		}
		routed, err := p.Router.RouteAccount(ctx, account)
		if err != nil {
			return err
		}
		flagged, err := flags.FlagAccount(ctx, p.DB, account, nil, p.Now)
		if err != nil {
			return err
		}
		log.Info("account routed", "account", account, "routed", routed, "flagged", flagged)
		if invites, candidates, err := p.Events(ctx, account); err != nil {
			log.Error("event scan failed", "account", account, logx.Err, err.Error())
		} else {
			log.Info("event scan done", "account", account, "invites", invites, "candidates", candidates)
		}
		applied, deleted, err := p.Applier.ApplyAll(ctx, account)
		if _, serr := p.Applier.ApplySnoozes(ctx, account); serr != nil {
			log.Error("snooze return failed", "account", account, logx.Err, serr.Error())
			failed = append(failed, account)
		}
		switch {
		case err != nil:
			log.Error("live pass failed", "account", account, logx.Err, err.Error())
			failed = append(failed, account)
		case applied.Ran && applied.Written == 0 && applied.Failed > 0 && deleted.Written == 0:
			log.Error("every live action failed", "account", account, "failed", applied.Failed)
			failed = append(failed, account)
		}
	}
	for _, extra := range p.Extras {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := extra.Run(ctx)
		if err != nil {
			log.Error("refresh failed", "name", extra.Name, logx.Err, err.Error())
			continue
		}
		log.Info("refreshed", "name", extra.Name, "result", result)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("the live pass failed for %s", strings.Join(failed, ", "))
	}
	return nil
}

const checkpointWait = time.Second

var passDeadline = 20 * time.Minute

func (p *Pass) Locked(waitCtx, workCtx context.Context, accounts []string) error {
	lock, err := db.Exclusive(waitCtx, p.Cfg.DBPath, 0)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	workCtx, cancel := context.WithTimeout(workCtx, passDeadline)
	defer cancel()
	defer p.checkpoint(workCtx)
	for _, account := range accounts {
		if err := p.SyncOne(workCtx, account); err != nil {
			return err
		}
	}
	return p.Process(workCtx, accounts)
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

func Sync(ctx context.Context, env config.Env, cfg config.Config, requested string, open mail.OpenFunc) ([]string, error) {
	accounts, err := SelectAccounts(ctx, requested)
	if err != nil {
		return nil, err
	}
	pass, release, err := Open(ctx, env, cfg, open, nil)
	if err != nil {
		return nil, err
	}
	defer release()
	return accounts, passrun.Track(ctx, pass.DB, passrun.Sync, func() error { return pass.Locked(ctx, ctx, accounts) })
}
