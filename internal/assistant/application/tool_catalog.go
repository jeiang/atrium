package application

import (
	"context"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/tool"
)

const ToolCatalogName application.Name = "assistant.tools"

type ToolCatalogInput struct{}

type ToolCatalogEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permission  string   `json:"permission" api:"enum=none|own|notify|people|internet|by_arguments"`
	Effect      string   `json:"effect" api:"enum=read|write_undo|propose|notify"`
	Kinds       []string `json:"kinds"`
}

type ToolCatalog struct {
	Items []ToolCatalogEntry `json:"items"`
}

func DescribeToolCatalog() application.Definition {
	return describeRead[ToolCatalogInput, ToolCatalog](ToolCatalogName)
}

func (s *Service) ToolCatalog(ctx context.Context, reg *registry.Registry) (application.Read[ToolCatalog], error) {
	tools, err := reg.All()
	if err != nil {
		return application.Read[ToolCatalog]{}, err
	}
	channels, _ := registry.PermissionAxes()
	out := ToolCatalog{Items: make([]ToolCatalogEntry, 0, len(tools))}
	for _, t := range tools {
		entry := ToolCatalogEntry{Name: t.Name, Description: t.Description, Permission: "none", Effect: string(t.Effect), Kinds: make([]string, 0, len(t.Contexts))}
		switch {
		case t.ChannelOf != nil:
			entry.Permission = "by_arguments"
		case t.Channel != tool.ChannelNone:
			entry.Permission = channels[t.Channel-1]
		}
		for _, k := range t.Contexts {
			entry.Kinds = append(entry.Kinds, string(k))
		}
		out.Items = append(out.Items, entry)
	}
	rev, err := s.snapshot(ctx, string(ToolCatalogName), []string{state.Settings}, out, func(db.Querier) error { return nil })
	return application.Read[ToolCatalog]{Revision: rev, Data: out}, err
}
