package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func reviewOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return append(reviewapp.Describe(), reviewapp.DescribePermissions()...), append(reviewRoutes(nil, nil), []application.Binding{
		{Kind: application.HTML, Target: "GET /review", Uses: uses(reviewapp.PendingName)},
		{Kind: application.HTML, Target: "GET /review/strip", Uses: uses(reviewapp.PendingName)},

		{Kind: application.HTML, Target: "GET /proposals/{proposal_id}", Uses: uses(reviewapp.ProposalName)},
		{Kind: application.HTML, Target: "GET /proposals/{proposal_id}/card", Uses: uses(reviewapp.ProposalName)},
		{Kind: application.CLI, Target: "proposals list", Uses: uses(reviewapp.ProposalsName)},
		{Kind: application.HTML, Target: "GET /activity", Uses: uses(reviewapp.ActivityName)},
		{Kind: application.HTML, Target: "GET /activity/recent", Uses: uses(reviewapp.ActivityName)},

		{Kind: application.CLI, Target: "effects list", Uses: uses(reviewapp.EffectsName)},
		{Kind: application.CLI, Target: "writes list", Uses: uses(reviewapp.EffectsName)},

		{Kind: application.CLI, Target: "effects show", Uses: uses(reviewapp.EffectName)},
		{Kind: application.HTML, Target: "POST /proposals/{proposal_id}/approve", Uses: uses(reviewapp.ApproveName)},
		{Kind: application.CLI, Target: "proposals approve", Uses: uses(reviewapp.ApproveName)},
		{Kind: application.HTML, Target: "POST /actions/{token}", Uses: []application.Name{reviewapp.ApproveName, reviewapp.DismissName}},
		{Kind: application.HTML, Target: "POST /proposals/{proposal_id}/dismiss", Uses: uses(reviewapp.DismissName)},
		{Kind: application.CLI, Target: "proposals dismiss", Uses: uses(reviewapp.DismissName)},
		{Kind: application.HTML, Target: "POST /proposals/{proposal_id}/undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.HTML, Target: "POST /activity/effect/{effect_id}/undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.HTML, Target: "POST /activity/mail/{action_id}/undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.CLI, Target: "proposals undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.CLI, Target: "effects undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.CLI, Target: "writes undo", Uses: uses(reviewapp.UndoName)},
		{Kind: application.HTML, Target: "POST /proposals/{proposal_id}/resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.HTML, Target: "POST /activity/effect/{effect_id}/resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.HTML, Target: "POST /activity/mail/{action_id}/resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.CLI, Target: "proposals resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.CLI, Target: "effects resolve", Uses: uses(reviewapp.ResolveName)},
		{Kind: application.CLI, Target: "writes resolve", Uses: uses(reviewapp.ResolveName)},
	}...)
}
