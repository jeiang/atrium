package application

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/subagents"
	"github.com/jeiang/atrium/internal/db"
)

const SubagentRolesName application.Name = "assistant.subagent_roles"

type SubagentRole struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Effort      string   `json:"effort"`
	Tools       []string `json:"tools"`
}

type SubagentRoleList struct {
	Items []SubagentRole `json:"items"`
}

func DescribeSubagentRoles() application.Definition {
	return application.Describe[struct{}, SubagentRoleList](SubagentRolesName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) ListSubagentRoles(ctx context.Context) (application.Read[SubagentRoleList], error) {
	out := SubagentRoleList{Items: make([]SubagentRole, len(subagents.Roles))}
	for i, r := range subagents.Roles {
		out.Items[i] = SubagentRole{Name: r.Name, Description: r.Description, Prompt: r.Prompt, Effort: r.Effort, Tools: slices.Clone(r.Tools)}
	}
	rev, err := s.snapshot(ctx, string(SubagentRolesName), []string{state.Settings}, out, func(db.Querier) error { return nil })
	return application.Read[SubagentRoleList]{Revision: rev, Data: out}, err
}
