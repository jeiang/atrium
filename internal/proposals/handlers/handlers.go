package handlers

import (
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mailops"
	"github.com/jeiang/atrium/internal/mail/openloops"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
)

type Deps struct {
	DB        *db.DB
	Open      mailops.Opener
	Syncer    *mailsync.Syncer
	Now       func() float64
	Env       config.Env
	NewNotion func(token string) *notion.Client
}

func Register(r *proposals.Registry, deps Deps) {
	env := deps.Env
	if env == nil {
		env = config.OS
	}
	executors.Register(r, &executors.Executors{DB: deps.DB, Open: deps.Open, Now: deps.Now}, deps.Syncer)
	calendar.Register(r, &calendar.Proposals{DB: deps.DB, Now: deps.Now, Env: env})
	(&notion.Applier{DB: deps.DB, Env: env, NewClient: deps.NewNotion, Now: deps.Now}).Register(r)
	openloops.Register(r, &openloops.Reminders{DB: deps.DB, Now: deps.Now})
}
