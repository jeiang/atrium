package application

import (
	"context"
	"database/sql"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	SecretStatusName application.Name = "settings.secret-status"
	SecretSetName    application.Name = "settings.secret-set"
	SecretDeleteName application.Name = "settings.secret-delete"
)

type SecretStatusInput struct {
	Names []string
}

type SecretState struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type SecretSetInput struct {
	Name  string
	Value string `json:"-"`
}

type SecretDeleteInput struct {
	Name string
}

func DescribeSecretStatus() application.Definition {
	return application.Describe[SecretStatusInput, []SecretState](SecretStatusName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeSecretSet() application.Definition {
	return application.Describe[SecretSetInput, SecretState](SecretSetName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeSecretDelete() application.Definition {
	return application.Describe[SecretDeleteInput, SecretState](SecretDeleteName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func (s *Service) SecretStatus(ctx context.Context, in SecretStatusInput) (application.Read[[]SecretState], error) {
	names := in.Names
	if len(names) == 0 {
		for _, def := range settings.From(ctx).Defs() {
			if def.IsSecret() {
				names = append(names, def.Key)
			}
		}
	}
	out := make([]SecretState, 0, len(names))
	for _, name := range names {
		state, err := s.secretState(ctx, s.store.Reading(), name)
		if err != nil {
			return application.Read[[]SecretState]{}, classifyRead(err)
		}
		out = append(out, state)
	}
	revision, err := s.revision(ctx, string(SecretStatusName), in)
	if err != nil {
		return application.Read[[]SecretState]{}, err
	}
	return application.Read[[]SecretState]{Revision: revision, Data: out}, nil
}

func classifyRead(err error) error {
	if classified := classify(err); classified.Code != application.Internal {
		return classified
	}
	return err
}

func (s *Service) secretState(ctx context.Context, st *settings.Store, name string) (SecretState, error) {
	if _, bound := secrets.FileBinding(ctx, name); bound {
		return SecretState{Name: name, Status: "file"}, nil
	}
	status, err := st.Status(ctx, name)
	if err != nil {
		return SecretState{}, err
	}
	return SecretState{Name: name, Status: string(status)}, nil
}

func (s *Service) SetSecret(ctx context.Context, in SecretSetInput) (application.Change[SecretState], error) {
	return s.secretCommand(ctx, SecretSetName, in.Name, func(st *settings.Store) (bool, error) {
		if err := st.Known(in.Name); err != nil {
			return false, err
		}
		if file, bound := secrets.FileBinding(ctx, in.Name); bound {
			return false, settings.AppKeyFile{Key: in.Name, File: file}
		}
		return true, st.Set(ctx, in.Name, in.Value, string(audit.SourceOf(ctx)))
	})
}

func (s *Service) DeleteSecret(ctx context.Context, in SecretDeleteInput) (application.Change[SecretState], error) {
	return s.secretCommand(ctx, SecretDeleteName, in.Name, func(st *settings.Store) (bool, error) {
		before, err := st.Status(ctx, in.Name)
		if err != nil {
			return false, err
		}
		if err := st.Delete(ctx, in.Name); err != nil {
			return false, err
		}
		return before != settings.Unset, nil
	})
}

func (s *Service) secretCommand(ctx context.Context, name application.Name, secret string, run func(*settings.Store) (bool, error)) (application.Change[SecretState], error) {
	id, err := s.command(ctx, name, secret, []string{secret}, func(tx *sql.Tx) (bool, error) {
		return run(s.store.Using(tx))
	})
	if err != nil {
		return application.Change[SecretState]{}, err
	}
	state, err := s.secretState(ctx, s.store.Reading(), secret)
	if err != nil {
		return application.Change[SecretState]{}, err
	}
	return application.Change[SecretState]{Data: state, AuditID: id}, nil
}
