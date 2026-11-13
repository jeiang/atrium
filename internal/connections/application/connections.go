package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/connections"
)

const (
	ListName          application.Name = "connections.list"
	TestSaveName      application.Name = "connections.test-save"
	SaveFieldsName    application.Name = "connections.save-fields"
	DisconnectName    application.Name = "connections.disconnect"
	TestName          application.Name = "connections.test"
	ObserveHealthName application.Name = "connections.observe-health"
)

type ID = connections.ID
type View = connections.View
type GoogleGrant = connections.GoogleGrant

type ListInput struct{}
type TestSaveInput struct {
	ID          ID
	Label       string
	URL         string
	Credentials map[string]string `json:"-"`
}
type SaveFieldsInput struct {
	ID        ID
	Candidate connections.Candidate `json:"-"`
}
type DisconnectInput struct{ ID ID }
type TestInput struct {
	ID  ID
	All bool
}
type ObserveHealthInput struct {
	ID    ID
	Stale bool
}

func DescribeList() application.Definition {
	return application.Describe[ListInput, []View](ListName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeTestSave() application.Definition {
	return application.Describe[TestSaveInput, View](TestSaveName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeSaveFields() application.Definition {
	return application.Describe[SaveFieldsInput, View](SaveFieldsName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeDisconnect() application.Definition {
	return application.Describe[DisconnectInput, struct{}](DisconnectName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeTest() application.Definition {
	return application.Describe[TestInput, []View](TestName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeObserveHealth() application.Definition {
	return application.Describe[ObserveHealthInput, struct{}](ObserveHealthName, application.Command, []application.Authority{application.System}, application.NoEffect, true)
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[[]View], error) {
	views, err := s.store.List(ctx)
	if err != nil {
		return application.Read[[]View]{}, err
	}
	revision, err := s.revision(ctx, ListName, in)
	return application.Read[[]View]{Revision: revision, Data: views}, err
}

func (s *Service) TestSave(ctx context.Context, in TestSaveInput) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, TestSaveName, []ID{in.ID}, func(commit connections.Commit) error {
		current, err := s.store.Get(ctx, in.ID)
		if err != nil {
			return err
		}
		cand := connections.Candidate{Label: in.Label, Config: current.Config}
		if cand.Label == "" {
			cand.Label = current.Label
		}
		for _, c := range current.Capabilities {
			cand.Capabilities = append(cand.Capabilities, connections.Capability(c))
		}
		if in.URL != "" {
			cfg := map[string]string{}
			if len(current.Config) > 0 {
				if err := json.Unmarshal(current.Config, &cfg); err != nil {
					return err
				}
			}
			if cfg == nil {
				cfg = map[string]string{}
			}
			cfg["url"] = in.URL
			cand.Config, err = json.Marshal(cfg)
			if err != nil {
				return err
			}
		}
		for slot, value := range in.Credentials {
			if slot != "" && value != "" {
				cand.Credentials = append(cand.Credentials, connections.CredentialInput{Slot: slot, Value: value})
			}
		}
		out, err = s.store.TestAndSave(ctx, in.ID, cand, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) SaveFields(ctx context.Context, in SaveFieldsInput) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, SaveFieldsName, []ID{in.ID}, func(commit connections.Commit) error {
		var err error
		out, err = s.store.Save(ctx, in.ID, in.Candidate, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) Disconnect(ctx context.Context, in DisconnectInput) (application.Change[struct{}], error) {
	auditID, err := s.command(ctx, DisconnectName, []ID{in.ID}, func(commit connections.Commit) error { return s.store.Disconnect(ctx, in.ID, commit) })
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) Test(ctx context.Context, in TestInput) (application.Change[[]View], error) {
	ids := []ID{in.ID}
	if in.All {
		views, err := s.store.List(ctx)
		if err != nil {
			return application.Change[[]View]{}, err
		}
		ids = nil
		for _, v := range views {
			if v.Enabled && v.State != connections.Unconfigured && v.State != connections.Disabled {
				ids = append(ids, v.ID)
			}
		}
	}
	var out []View
	auditID, err := s.command(ctx, TestName, ids, func(commit connections.Commit) error {
		var err error
		out, err = s.pass(ctx, ids, false, commit)
		return err
	})
	return application.Change[[]View]{Data: out, AuditID: auditID}, err
}

func (s *Service) ObserveHealth(ctx context.Context, in ObserveHealthInput) (application.Change[struct{}], error) {
	views, err := s.store.List(ctx)
	if err != nil {
		return application.Change[struct{}]{}, err
	}
	var ids []ID
	for _, v := range views {
		if in.ID != "" {
			if v.ID != in.ID || !recheckable(v) {
				continue
			}
		} else if !v.Enabled || v.State == connections.Unconfigured || v.State == connections.Disabled || in.Stale && v.CheckedAt == nil {
			continue
		}
		ids = append(ids, v.ID)
	}
	if len(ids) == 0 {
		return application.Change[struct{}]{}, nil
	}
	auditID, err := s.command(ctx, ObserveHealthName, ids, func(commit connections.Commit) error {
		_, err := s.pass(ctx, ids, true, commit)
		return err
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) pass(ctx context.Context, ids []ID, silent bool, commit connections.Commit) ([]View, error) {
	var out []View
	changed := false
	for i, id := range ids {
		nextChanged := changed
		record := func(ctx context.Context, tx *sql.Tx, updated bool) error {
			nextChanged = changed || updated
			if i == len(ids)-1 {
				return commit(ctx, tx, nextChanged)
			}
			return nil
		}
		var view View
		var err error
		if silent {
			view, err = s.store.CheckOne(ctx, id, record)
		} else {
			view, err = s.store.Test(ctx, id, record)
		}
		if err != nil {
			var committed *connections.CommittedError
			if i == len(ids)-1 && errors.As(err, &committed) {
				return out, err
			}
			if changed || (nextChanged && errors.As(err, &committed)) {
				return out, &partialError{cause: err}
			}
			return out, err
		}
		changed = nextChanged
		if !silent {
			out = append(out, view)
		}
	}
	return out, nil
}

func recheckable(v View) bool {
	switch v.State {
	case connections.Failing, connections.NeedsReauth, connections.Disabled, connections.Unconfigured:
		return false
	}
	return v.CheckedAt != nil && time.Since(*v.CheckedAt) >= 5*time.Minute
}
