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
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/flags"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/openloops"
	"github.com/jeiang/atrium/internal/mail/router"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/mail/watches"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

var log = logx.Logger("atrium.run")

type MissingCredentials string

func (e MissingCredentials) Error() string { return "missing credentials for " + string(e) }

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

type Extra struct {
	Name string
	Run  func(ctx context.Context) (any, error)
}

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

func New(d *db.DB, cfg config.Config, open mailops.Opener, now func() float64) *Pass {
	if now == nil {
		now = wallClock
	}
	open = mailops.Opener(mail.BindMode(mail.OpenFunc(open), d.Read))
	syncer := &mailsync.Syncer{DB: d, Cfg: cfg, Now: now}
	l := &live.Live{DB: d, Cfg: cfg, Now: now}
	p := &Pass{
		DB:      d,
		Cfg:     cfg,
		Open:    open,
		Now:     now,
		Syncer:  syncer,
		Live:    l,
		Router:  &router.Router{DB: d, Now: now},
		Applier: applier.New(l, open, syncer),
		Events: func(ctx context.Context, account string) (int, int, error) {
			if on, err := module.FlagOn(ctx, d.Read, module.Calendar); err != nil || !on {
				return 0, 0, err
			}
			return candidates.ScanAccount(ctx, d, cfg, account, nil, config.OS, now)
		},
	}
	p.Extras = []Extra{
		{"open loops", func(ctx context.Context) (any, error) {
			return openloops.Refresh(ctx, d, cfg, now(), openloops.Options{Clock: now}), nil
		}},
		{"watches", func(ctx context.Context) (any, error) {
			return watches.Refresh(ctx, d, now(), watches.Options{Clock: now}), nil
		}},
		{"money", func(ctx context.Context) (any, error) {
			if on, err := module.FlagOn(ctx, d.Read, module.Money); err != nil || !on {
				return py.Dict{{Key: "skipped", Value: "money module is not enabled"}}, err
			}
			limit, err := money.ScanLimit(ctx, d.Read)
			if err != nil {
				return nil, err
			}
			return money.Refresh(ctx, d, limit, now(), p.Money)
		}},
	}
	return p
}

func wallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func (p *Pass) SyncOne(ctx context.Context, account string) error {
	_, err := p.Syncer.One(ctx, account, mailsync.Opener(p.Open))
	return err
}

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
	embedURL, err := mail.EmbedURL(ctx, p.DB.Read)
	if err != nil {
		return err
	}
	ctx, err = mail.WithModels(ctx, p.DB.Read)
	if err != nil {
		return err
	}
	if err := embed.PendingOrWarn(ctx, p.DB, embedURL, log); err != nil {
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
		flagged, err := flags.FlagAccount(ctx, p.DB, account, p.Judge, p.Now)
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
	return ctx.Err()
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

func Sync(ctx context.Context, cfg config.Config, requested string, open mailops.Opener) ([]string, error) {
	d, err := db.Connect(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	if err := config.Migrate(ctx, d, cfg); err != nil {
		return nil, err
	}
	accounts, err := SelectAccounts(cfg, requested)
	if err != nil {
		return nil, err
	}
	return accounts, New(d, cfg, open, nil).Locked(ctx, ctx, accounts)
}
