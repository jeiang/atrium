package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func reviewOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return append(reviewapp.Describe(), reviewapp.DescribePermissions()...), append(reviewRoutes(nil, nil), []application.Binding{
		{Kind: application.CLI, Target: "proposals list", Uses: uses(reviewapp.ProposalsName)},

		{Kind: application.CLI, Target: "effects list", Uses: uses(reviewapp.EffectsName)},

		{Kind: application.CLI, Target: "effects show", Uses: uses(reviewapp.EffectName)},
		{Kind: application.CLI, Target: "proposals approve", Uses: uses(reviewapp.ApproveName)},
		{Kind: application.CLI, Target: "proposals dismiss", Uses: uses(reviewapp.DismissName)},
		{Kind: application.CLI, Target: "proposals undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.CLI, Target: "effects undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.CLI, Target: "proposals resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.CLI, Target: "effects resolve", Uses: uses(reviewapp.ResolveName)},
	}...)
}
