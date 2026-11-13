package application

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Querier = db.Querier

type Registry = settings.Registry

type Group = settings.Group

type GroupView = settings.GroupView

type ModuleView = settings.ModuleView

func From(ctx context.Context) *Registry {
	return settings.From(ctx)
}

func LoadFields(ctx context.Context, q Querier, fields []Field) ([]FieldView, error) {
	return settings.LoadFields(ctx, q, fields)
}

func EchoFields(ctx context.Context, fields []FieldView, values map[string]string) {
	settings.EchoFields(ctx, fields, values)
}
