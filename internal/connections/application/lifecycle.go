package application

import (
	"context"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

const (
	EnableName  application.Name = "connections.enable"
	DisableName application.Name = "connections.disable"

	RecheckInterval = 5 * time.Minute
)

type EnableInput struct{ ID ID }
type DisableInput struct{ ID ID }
type RecheckInput struct{ ID ID }

func DescribeEnable() application.Definition {
	return application.Describe[EnableInput, View](EnableName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeDisable() application.Definition {
	return application.Describe[DisableInput, View](DisableName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (s *Service) Enable(ctx context.Context, in EnableInput) (application.Change[View], error) {
	return s.setEnabled(ctx, EnableName, in.ID, true)
}

func (s *Service) Disable(ctx context.Context, in DisableInput) (application.Change[View], error) {
	return s.setEnabled(ctx, DisableName, in.ID, false)
}

func (s *Service) setEnabled(ctx context.Context, name application.Name, id ID, enabled bool) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, name, []ID{id}, func(commit connections.Commit) error {
		var err error
		out, err = s.store.SetEnabled(ctx, id, enabled, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) SyncUnits(ctx context.Context) error { return s.store.SyncUnits(ctx) }

func (s *Service) UnitsChanged() <-chan struct{} { return s.store.UnitsChanged() }

type recheckKey struct {
	d  *db.DB
	id ID
}

var rechecks sync.Map

func refuse(message string) error {
	return &application.Error{Code: application.Refused, Message: message}
}

func (s *Service) Recheck(ctx context.Context, in RecheckInput) (View, error) {
	view, err := s.store.Get(ctx, in.ID)
	if err != nil {
		return View{}, classify(err)
	}
	if !view.Enabled {
		return View{}, refuse(string(in.ID) + " is disabled; only the operator can turn it on")
	}
	if len(view.Dependents) == 0 || view.Idle != "" {
		return View{}, refuse(string(in.ID) + " serves no need of an app that is on")
	}
	if view.State == connections.NeedsReauth && !credentialChangedSince(view) {
		return View{}, refuse(string(in.ID) + " needs the operator to sign in again; send the fix link instead of rechecking")
	}
	now := time.Now()
	key := recheckKey{d: s.store.DB(), id: in.ID}
	for {
		last, seen := rechecks.LoadOrStore(key, now)
		if !seen {
			break
		}
		if wait := last.(time.Time).Add(RecheckInterval).Sub(now); wait > 0 {
			return View{}, refuse(string(in.ID) + " was rechecked recently; try again in " + wait.Round(time.Second).String())
		}
		if rechecks.CompareAndSwap(key, last, now) {
			break
		}
	}
	change, err := s.Test(ctx, TestInput{ID: in.ID})
	if err != nil {
		return View{}, err
	}
	if len(change.Data) == 0 {
		return s.store.Get(ctx, in.ID)
	}
	return change.Data[0], nil
}

func credentialChangedSince(v View) bool {
	if v.CheckedAt == nil {
		return true
	}
	for _, c := range v.Credentials {
		if c.SetAt != nil && c.SetAt.After(*v.CheckedAt) {
			return true
		}
	}
	return false
}

type State = connections.State

func Class(state State) string {
	if c := connections.RepairClass(state); c != "" {
		return c
	}
	switch state {
	case connections.Disabled:
		return "disabled"
	case connections.Untested:
		return "untested"
	}
	return ""
}

const (
	StatusToolName  application.Name = "connections.dependency_status"
	RecheckToolName application.Name = "connections.dependency_recheck"
)

func DescribeStatusTool() application.Definition {
	return application.Describe[DependenciesInput, Dependencies](StatusToolName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}

func DescribeRecheckTool() application.Definition {
	return application.Describe[RecheckInput, View](RecheckToolName, application.Command, []application.Authority{application.Agent}, application.NoEffect, false)
}
