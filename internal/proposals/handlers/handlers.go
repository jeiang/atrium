// Package handlers is handlers.py: the registration of every proposal kind's executor and undoers, in the order
// Python registers them. The Notion, calendar and open-loop kinds join here from their slices; the wiki-plan
// kind is not registered.
package handlers

import (
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/mailops"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/proposals/executors"
)

// Deps is what the registered executors and undoers run against: the database, the account opener, the
// syncer of the post-write resync, and the clock of the action log (time.Now when nil).
type Deps struct {
	DB     *db.DB
	Open   mailops.Opener
	Syncer *mailsync.Syncer
	Now    func() float64
}

// Register is register.
func Register(r *proposals.Registry, deps Deps) {
	executors.Register(r, &executors.Executors{DB: deps.DB, Open: deps.Open, Now: deps.Now}, deps.Syncer)
}
