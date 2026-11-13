package application

import (
	"context"
	"errors"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/davmirror"
)

const defaultLimit = 5

var includeFields = []string{"emails", "phones", "dates"}

type LookupInput struct {
	Query   string   `json:"query"`
	Limit   *int64   `json:"limit,omitempty"`
	Include []string `json:"include,omitempty"`
}

type Lookup struct {
	Value map[string]any `json:"value"`
	PII   bool           `json:"-"`
}

type SyncInput struct{}

type SyncResult = davmirror.Stats

func notConfigured() error {
	return &application.Error{Code: application.NotConfigured, Message: contacts.DirKey + " is not set"}
}

func (s *Service) Lookup(ctx context.Context, in LookupInput) (application.Read[Lookup], error) {
	maxContacts, err := contacts.MaxResults(ctx, s.db.Read)
	if err != nil {
		return application.Read[Lookup]{}, err
	}
	limit := int64(defaultLimit)
	if in.Limit != nil {
		limit = *in.Limit
	}
	limit = max(1, min(limit, maxContacts))
	include := map[string]bool{}
	for _, name := range in.Include {
		if !slices.Contains(includeFields, name) {
			return application.Read[Lookup]{}, &application.Error{Code: application.Invalid, Message: "include must be a list of emails, phones or dates"}
		}
		include[name] = true
	}
	src, err := contacts.Resolve(ctx, s.db.Read)
	if err != nil {
		return application.Read[Lookup]{}, err
	}
	if src.State != contacts.Ready {
		return application.Read[Lookup]{}, notConfigured()
	}
	found := contacts.Search(src.Dir, in.Query, int(limit))
	results := make([]any, len(found))
	pii := false
	for i, c := range found {
		card := map[string]any{}
		if c.Name != "" {
			card["name"] = c.Name
		}
		if c.Org != "" {
			card["org"] = c.Org
		}
		if include["emails"] && len(c.Emails) > 0 {
			card["emails"] = c.Emails
			pii = true
		}
		if include["phones"] && len(c.Phones) > 0 {
			card["phones"] = c.Phones
			pii = true
		}
		if include["dates"] && len(c.Dates) > 0 {
			dates := make([]any, len(c.Dates))
			for j, d := range c.Dates {
				var year any
				if d.Year != nil {
					year = int64(*d.Year)
				}
				dates[j] = map[string]any{"kind": d.Kind, "label": d.Label, "month": int64(d.Month),
					"day": int64(d.Day), "year": year}
			}
			card["dates"] = dates
			pii = true
		}
		results[i] = card
	}
	out := Lookup{Value: map[string]any{"results": results}, PII: pii}
	counters, err := s.state.Read(ctx, nil, []string{state.Settings})
	if err != nil {
		return application.Read[Lookup]{}, err
	}
	rev := state.Revision(string(LookupName), counters, map[string]any{"input": in, "data": out.Value}, "")
	return application.Read[Lookup]{Revision: application.Revision(rev), Data: out}, nil
}

func (s *Service) Sync(ctx context.Context, _ SyncInput) (application.Change[SyncResult], error) {
	set, err := calendar.LoadSettings(ctx, s.db.Read)
	if err != nil {
		s.audit(ctx, SyncName, state.Failed, map[string]any{})
		return application.Change[SyncResult]{}, err
	}
	src, err := contacts.Resolve(ctx, s.db.Read)
	if err != nil {
		s.audit(ctx, SyncName, state.Failed, map[string]any{})
		return application.Change[SyncResult]{}, err
	}
	switch src.State {
	case contacts.Off:
		return application.Change[SyncResult]{AuditID: s.audit(ctx, SyncName, state.Noop, map[string]any{"reason": "off"})}, nil
	case contacts.Unset:
		s.audit(ctx, SyncName, state.Refused, map[string]any{"reason": "not_configured"})
		return application.Change[SyncResult]{}, config.NotConfigured(contacts.DirKey + " is not set")
	}
	err = calendar.MirrorConfigured(ctx, set, src.Dir)
	var stats davmirror.Stats
	if err == nil {
		err = passrun.Track(ctx, s.db, passrun.CardDAV, func() error {
			var syncErr error
			stats, syncErr = calendar.SyncContactsMirror(ctx, s.env, set, src.Dir)
			return syncErr
		})
	}
	if err != nil {
		outcome := state.Failed
		if ctx.Err() != nil {
			outcome = state.Canceled
		}
		s.audit(ctx, SyncName, outcome, map[string]any{})
		return application.Change[SyncResult]{}, err
	}
	return application.Change[SyncResult]{Data: stats, AuditID: s.audit(ctx, SyncName, state.OK, map[string]any{})}, nil
}

func (s *Service) SyncManual(ctx context.Context, cfg config.Config) (SyncResult, error) {
	var out SyncResult
	var syncErr error
	_, err := module.Invoke(ctx, s.db, cfg, module.Contacts+"/"+passrun.CardDAV, db.ShortLockWait, func(ctx context.Context, _ scheduler.Run) (scheduler.Result, error) {
		change, runErr := s.Sync(ctx, SyncInput{})
		out, syncErr = change.Data, runErr
		if missing, ok := errors.AsType[config.NotConfigured](runErr); ok {
			return scheduler.Result{Status: scheduler.StatusSkippedUnconfigured, Error: new(missing.Error())}, nil
		}
		return scheduler.Result{}, runErr
	})
	if err == nil {
		err = syncErr
	}
	return out, err
}
