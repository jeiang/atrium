package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
	CheckName         application.Name = "connections.check"
	DependenciesName  application.Name = "connections.dependencies"
)

type ID = connections.ID
type View = connections.View
type GoogleGrant = connections.GoogleGrant
type Dependencies = connections.Dependencies

type ListInput struct{}
type DependenciesInput struct{}
type TestSaveInput struct {
	ID          ID
	Label       string
	URL         string
	Credentials map[string]string `json:"-"`
}
type SaveFieldsInput struct {
	ID          ID
	Label       string
	URL         string
	Credentials map[string]string `json:"-"`
}
type DisconnectInput struct{ ID ID }
type TestInput struct {
	ID  ID
	All bool
}
type TraceInput struct {
	ID         ID
	Capability string
	Repeat     int
	Out        io.Writer
}
type ObserveHealthInput struct {
	ID        ID
	Recovered bool
	// Capability is the capability a runtime request used, when known.
	Capability string
	Start      bool
}
type CheckInput struct {
	Changed bool
}

func DescribeList() application.Definition {
	return application.Describe[ListInput, []View](ListName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeDependencies() application.Definition {
	return application.Describe[DependenciesInput, Dependencies](DependenciesName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
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
func DescribeCheck() application.Definition {
	return application.Describe[CheckInput, []View](CheckName, application.Command, []application.Authority{application.System}, application.NoEffect, true)
}

func (s *Service) List(ctx context.Context, in ListInput) (application.Read[[]View], error) {
	views, err := s.store.List(ctx)
	if err != nil {
		return application.Read[[]View]{}, err
	}
	revision, err := s.revision(ctx, ListName, in)
	return application.Read[[]View]{Revision: revision, Data: views}, err
}

func (s *Service) Dependencies(ctx context.Context, in DependenciesInput) (application.Read[Dependencies], error) {
	deps, err := s.store.Dependencies(ctx)
	if err != nil {
		return application.Read[Dependencies]{}, err
	}
	revision, err := s.revision(ctx, DependenciesName, in)
	return application.Read[Dependencies]{Revision: revision, Data: deps}, err
}

func (s *Service) TestSave(ctx context.Context, in TestSaveInput) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, TestSaveName, []ID{in.ID}, func(commit connections.Commit) error {
		cand, err := s.candidate(ctx, in.ID, in.Label, in.URL, in.Credentials)
		if err != nil {
			return err
		}
		out, err = s.store.TestAndSave(ctx, in.ID, cand, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) SaveFields(ctx context.Context, in SaveFieldsInput) (application.Change[View], error) {
	var out View
	auditID, err := s.command(ctx, SaveFieldsName, []ID{in.ID}, func(commit connections.Commit) error {
		cand, err := s.candidate(ctx, in.ID, in.Label, in.URL, in.Credentials)
		if err != nil {
			return err
		}
		out, err = s.store.Save(ctx, in.ID, cand, commit)
		return err
	})
	return application.Change[View]{Data: out, AuditID: auditID}, err
}

func (s *Service) candidate(ctx context.Context, id ID, label, url string, credentials map[string]string) (connections.Candidate, error) {
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return connections.Candidate{}, err
	}
	cand := connections.Candidate{Label: label, Config: current.Config}
	if cand.Label == "" {
		cand.Label = current.Label
	}
	for _, c := range current.Capabilities {
		cand.Capabilities = append(cand.Capabilities, connections.Capability(c))
	}
	if url != "" {
		cfg := map[string]string{}
		if len(current.Config) > 0 {
			if err := json.Unmarshal(current.Config, &cfg); err != nil {
				return connections.Candidate{}, err
			}
		}
		if cfg == nil {
			cfg = map[string]string{}
		}
		cfg["url"] = url
		cand.Config, err = json.Marshal(cfg)
		if err != nil {
			return connections.Candidate{}, err
		}
	}
	for slot, value := range credentials {
		cand.Credentials = append(cand.Credentials, connections.CredentialInput{Slot: slot, Value: value})
	}
	return cand, nil
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
			if checkable(v) {
				ids = append(ids, v.ID)
			}
		}
	}
	var out []View
	auditID, err := s.command(ctx, TestName, ids, func(commit connections.Commit) error {
		var err error
		out, err = s.pass(ctx, ids, in.All, commit)
		return err
	})
	return application.Change[[]View]{Data: out, AuditID: auditID}, err
}

func (s *Service) Trace(ctx context.Context, in TraceInput) (connections.Result, error) {
	return s.store.Trace(ctx, in.ID, connections.TraceRequest{Capability: in.Capability, Repeat: in.Repeat, Out: in.Out})
}

func (s *Service) ObserveHealth(ctx context.Context, in ObserveHealthInput) (application.Change[struct{}], error) {
	if in.Start {
		if err := s.store.AnnounceRemoved(ctx); err != nil {
			return application.Change[struct{}]{}, err
		}
	}
	views, err := s.store.List(ctx)
	if err != nil {
		return application.Change[struct{}]{}, err
	}
	var ids, checked []ID
	for _, v := range views {
		if in.ID != "" && in.Recovered && v.ID == in.ID && v.Idle == "" && capabilityFailed(v, in.Capability) {
			// A real request to the failing capability worked: that is the
			// recovery, so no check request is sent.
			return application.Change[struct{}]{}, s.store.Recover(ctx, v, in.Capability)
		}
		if !checkable(v) || v.Idle != "" || in.ID != "" && !wanted(v, in) || in.ID == "" && !due(v, in.Start) {
			continue
		}
		if v.CheckedAt == nil {
			ids = append(ids, v.ID)
		} else {
			checked = append(checked, v.ID)
		}
	}
	ids = append(ids, checked...)
	if len(ids) == 0 {
		return application.Change[struct{}]{}, nil
	}
	auditID, err := s.command(ctx, ObserveHealthName, ids, func(commit connections.Commit) error {
		_, err := s.pass(ctx, ids, true, commit)
		return err
	})
	return application.Change[struct{}]{AuditID: auditID}, err
}

func (s *Service) Check(ctx context.Context, in CheckInput) (application.Change[[]View], error) {
	views, err := s.store.List(ctx)
	if err != nil {
		return application.Change[[]View]{}, err
	}
	var ids []ID
	for _, v := range views {
		if checkable(v) && v.Idle == "" && (!in.Changed || v.CheckedAt == nil) {
			ids = append(ids, v.ID)
		}
	}
	if len(ids) == 0 {
		return application.Change[[]View]{Data: []View{}}, nil
	}
	var out []View
	auditID, err := s.command(ctx, CheckName, ids, func(commit connections.Commit) error {
		var err error
		out, err = s.pass(ctx, ids, true, commit)
		return err
	})
	return application.Change[[]View]{Data: out, AuditID: auditID}, err
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
		out = append(out, view)
	}
	return out, nil
}

const sweepInterval = 24 * time.Hour

func checkable(v View) bool {
	return v.Enabled && v.State != connections.Unconfigured && v.State != connections.Disabled
}

func wanted(v View, in ObserveHealthInput) bool {
	if v.ID != in.ID || backingOff(v, time.Now()) {
		return false
	}
	if in.Recovered {
		return failed(v) && olderThan(v, time.Minute)
	}
	return olderThan(v, 5*time.Minute)
}

func capabilityFailed(v View, capability string) bool {
	for _, r := range v.Results {
		if capability != "" && r.Capability == capability {
			return r.State == connections.Failing || r.State == connections.NeedsReauth
		}
	}
	return false
}

func olderThan(v View, age time.Duration) bool {
	return v.CheckedAt == nil || time.Since(*v.CheckedAt) >= age
}

func due(v View, start bool) bool {
	return olderThan(v, sweepInterval) || v.GraceLapsed() || start && failed(v) && !backingOff(v, time.Now())
}

func failed(v View) bool {
	return v.State == connections.Failing || v.State == connections.NeedsReauth
}

// backingOff reports whether an automatic recheck of a failing connection
// must wait: a sibling capability's success, a runtime failure or a boot
// does not check it again before its failure backoff ends. Explicit tests,
// checks and credential changes do not consult it.
func backingOff(v View, now time.Time) bool {
	return failed(v) && v.CheckedAt != nil && now.Before(v.CheckedAt.Add(failureBackoff(v)))
}

// failureBackoff is 1 h, 3 h, 6 h or 12 h by how long the connection had
// been failing at its last check, measured from its last success, or from
// the start of its reauthorization need.
func failureBackoff(v View) time.Duration {
	since := v.LastOKAt
	if v.State == connections.NeedsReauth && v.ReauthAt != nil {
		since = v.ReauthAt
	}
	var failing time.Duration
	if since != nil && v.CheckedAt != nil {
		failing = v.CheckedAt.Sub(*since)
	}
	switch {
	case failing < time.Hour:
		return time.Hour
	case failing < 3*time.Hour:
		return 3 * time.Hour
	case failing < 6*time.Hour:
		return 6 * time.Hour
	}
	return 12 * time.Hour
}
