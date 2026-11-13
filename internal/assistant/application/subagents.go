package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/pricing"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/db"
)

const (
	SubagentSetName     application.Name = "assistant.subagent.set"
	SubagentEnabledName application.Name = "assistant.subagent.enabled"
	SubagentDeleteName  application.Name = "assistant.subagent.delete"
	SubagentListName    application.Name = "assistant.subagent.list"
)

var (
	operatorOnly   = []application.Authority{application.Operator}
	subagentScopes = []string{state.Assistant}
)

type Subagent struct {
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Builtin bool     `json:"builtin"`
	Effort  string   `json:"effort"`
	Tools   []string `json:"tools"`
}

type SubagentSetInput struct {
	Name        string
	Description *string
	Prompt      *string
	Effort      *string
	Tools       []string
}

type SubagentEnabledInput struct {
	Name    string
	Enabled bool
}

type SubagentDeleteInput struct {
	Name string
}

type SubagentListInput struct{}

func DescribeSubagentSet() application.Definition {
	return application.Describe[SubagentSetInput, Subagent](SubagentSetName, application.Command, operatorOnly, application.NoEffect, true)
}

func DescribeSubagentEnabled() application.Definition {
	return application.Describe[SubagentEnabledInput, Subagent](SubagentEnabledName, application.Command, operatorOnly, application.NoEffect, true)
}

func DescribeSubagentDelete() application.Definition {
	return application.Describe[SubagentDeleteInput, Subagent](SubagentDeleteName, application.Command, operatorOnly, application.NoEffect, true)
}

func DescribeSubagentList() application.Definition {
	return application.Describe[SubagentListInput, []Subagent](SubagentListName, application.Query, operatorOnly, application.NoEffect, false)
}

func (s *Service) WithTools(src registry.Source) *Service {
	s.tools = src
	return s
}

func subagentOf(a subagents.Agent) Subagent {
	return Subagent{Name: a.Name, Enabled: a.Enabled, Builtin: a.Builtin, Effort: a.Effort, Tools: a.Tools}
}

func unknownSubagent(name string) error {
	return notFound(fmt.Sprintf("no subagent %q", name))
}

func subagentRefusal(code application.Code, err error) error {
	var refused subagents.Error
	if errors.As(err, &refused) {
		return &application.Error{Code: code, Message: refused.Error(), Cause: err}
	}
	return err
}

func (s *Service) SubagentList(ctx context.Context, in SubagentListInput) (application.Read[[]Subagent], error) {
	out := []Subagent{}
	rev, err := s.snapshot(ctx, string(SubagentListName), subagentScopes, in, func(q db.Querier) error {
		found, err := subagents.List(ctx, q)
		for _, a := range found {
			out = append(out, subagentOf(a))
		}
		return err
	})
	return application.Read[[]Subagent]{Revision: rev, Data: out}, err
}

func (s *Service) subagentCommand(ctx context.Context, name application.Name, agent string, code application.Code, apply func(tx *sql.Tx) (Subagent, error)) (application.Change[Subagent], error) {
	var out Subagent
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if out, err = apply(tx); err != nil {
			return err
		}
		auditID, err = s.audit(ctx, tx, name, "subagent:"+agent, state.OK)
		return err
	})
	if err != nil {
		return application.Change[Subagent]{}, s.reject(ctx, name, "subagent:"+agent, subagentRefusal(code, err))
	}
	return application.Change[Subagent]{Data: out, AuditID: auditID}, nil
}

func (s *Service) SubagentSet(ctx context.Context, in SubagentSetInput) (application.Change[Subagent], error) {
	reg, err := s.tools()
	if err != nil {
		return application.Change[Subagent]{}, err
	}
	now := s.now()
	return s.subagentCommand(ctx, SubagentSetName, in.Name, application.Refused, func(tx *sql.Tx) (Subagent, error) {
		existing, err := subagents.Get(ctx, tx, in.Name)
		if err != nil {
			return Subagent{}, err
		}
		def := subagents.Definition{Name: in.Name, Effort: pricing.Medium, Tools: in.Tools, Enabled: true}
		if existing != nil {
			def = subagents.Definition{Name: in.Name, Description: existing.Description, Prompt: existing.Prompt,
				Effort: existing.Effort, Tools: existing.Tools, Enabled: existing.Enabled}
			if in.Tools != nil {
				def.Tools = in.Tools
			}
		}
		if in.Description != nil {
			def.Description = *in.Description
		}
		if in.Prompt != nil {
			def.Prompt = *in.Prompt
		}
		if in.Effort != nil {
			def.Effort = *in.Effort
		}
		saved, err := subagents.Save(ctx, tx, reg, def, now)
		if err != nil {
			return Subagent{}, err
		}
		return subagentOf(*saved), nil
	})
}

func (s *Service) SubagentEnabled(ctx context.Context, in SubagentEnabledInput) (application.Change[Subagent], error) {
	now := s.now()
	return s.subagentCommand(ctx, SubagentEnabledName, in.Name, application.Refused, func(tx *sql.Tx) (Subagent, error) {
		changed, err := subagents.SetEnabled(ctx, tx, in.Name, in.Enabled, now)
		if err != nil {
			return Subagent{}, err
		}
		if !changed {
			return Subagent{}, unknownSubagent(in.Name)
		}
		found, err := subagents.Get(ctx, tx, in.Name)
		if err != nil || found == nil {
			return Subagent{}, errors.Join(err, unknownSubagent(in.Name))
		}
		return subagentOf(*found), nil
	})
}

func (s *Service) SubagentDelete(ctx context.Context, in SubagentDeleteInput) (application.Change[Subagent], error) {
	return s.subagentCommand(ctx, SubagentDeleteName, in.Name, application.Refused, func(tx *sql.Tx) (Subagent, error) {
		found, err := subagents.Get(ctx, tx, in.Name)
		if err != nil {
			return Subagent{}, err
		}
		if found == nil {
			return Subagent{}, unknownSubagent(in.Name)
		}
		removed, err := subagents.Delete(ctx, tx, in.Name)
		if err != nil {
			return Subagent{}, err
		}
		if !removed {
			return Subagent{}, unknownSubagent(in.Name)
		}
		return subagentOf(*found), nil
	})
}
