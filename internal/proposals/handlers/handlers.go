// Package handlers is handlers.py: the registration of every proposal kind's executor and undoers, in the order
// Python registers them. The Notion kinds join here from their slice; the wiki-plan
// kind is not registered.
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

// Deps is what the registered executors and undoers run against: the database, the account opener, the
// syncer of the post-write resync, and the clock of the action log (time.Now when nil). A later slice that
// registers a kind adds the fields its handlers need here and its registration call to Register, at its
// place in Python's order; nothing registers handlers outside Register.
type Deps struct {
	DB        *db.DB
	Open      mailops.Opener
	Syncer    *mailsync.Syncer
	Now       func() float64
	Env       config.Env
	NewNotion func(token string) *notion.Client
}

// Register is register: executors, then (as their slices land) notion_proposals, then calendar_proposals and
// openloops (the reminder kind). notion_migrate (wiki-plan) is not registered.
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
