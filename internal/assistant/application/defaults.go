package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const DefaultsSetName application.Name = "assistant.defaults-set"

type DefaultsSetInput struct {
	Model        string
	Effort       string
	ContextLimit string
}

type DefaultsChanged struct {
	Model        string `json:"model"`
	Effort       string `json:"effort"`
	ContextLimit int64  `json:"context_limit"`
}

func DescribeDefaultsSet() application.Definition {
	return application.Describe[DefaultsSetInput, DefaultsChanged](DefaultsSetName, application.Command,
		[]application.Authority{application.Operator}, application.NoEffect, true)
}

func OperatorUI(ctx context.Context) context.Context {
	return audit.With(ctx, "operator", audit.UI)
}

func defaultsInvalid(err error) error {
	var refused threads.OptionError
	if errors.As(err, &refused) {
		return &application.Error{Code: application.Invalid, Message: refused.Error(), Cause: err}
	}
	return err
}

func (s *Service) DefaultsSet(ctx context.Context, in DefaultsSetInput) (application.Change[DefaultsChanged], error) {
	o, err := threads.CleanDefaults(ctx, s.db.Read, in.Model, in.Effort, in.ContextLimit)
	if err != nil {
		return application.Change[DefaultsChanged]{}, s.reject(ctx, DefaultsSetName, "", defaultsInvalid(err))
	}
	var auditID int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		if err := threads.WriteDefaults(ctx, tx, o); err != nil {
			return err
		}
		auditID, err = s.audit(ctx, tx, DefaultsSetName, "", state.OK)
		return err
	})
	if err != nil {
		return application.Change[DefaultsChanged]{}, s.reject(ctx, DefaultsSetName, "", defaultsInvalid(err))
	}
	return application.Change[DefaultsChanged]{Data: DefaultsChanged{Model: o.Model, Effort: o.Effort, ContextLimit: o.ContextLimit}, AuditID: auditID}, nil
}
