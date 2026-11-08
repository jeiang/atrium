package handlers

import (
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/openloops"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
)

type Deps struct {
	DB     *db.DB
	Open   mail.OpenFunc
	Syncer *mailsync.Syncer
	Now    func() float64
	Env    config.Env
}

func Register(r *proposals.Registry, deps Deps) {
	executors.Register(r, &executors.Executors{DB: deps.DB, Open: deps.Open, Now: deps.Now}, deps.Syncer, module.Mail)
	calendar.Register(r, &calendar.Proposals{DB: deps.DB, Now: deps.Now, Env: deps.Env}, module.Calendar)
	(&notion.Applier{DB: deps.DB, Env: deps.Env, Now: deps.Now}).Register(r, module.Notion)
	openloops.Register(r, &openloops.Reminders{DB: deps.DB, Now: deps.Now}, module.Mail)
	profiletools.RegisterProposal(r, &profiletools.UpdateExecutor{DB: deps.DB, Now: deps.Now}, module.Assistant)
}
