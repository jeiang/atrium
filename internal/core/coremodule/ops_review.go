package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	reviewapp "github.com/jeiang/atrium/internal/review/application"
)

func reviewOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return reviewapp.Describe(), []application.Binding{
		{Kind: application.HTML, Target: "GET /review", Uses: uses(reviewapp.PendingName)},
		{Kind: application.HTML, Target: "GET /review/strip", Uses: uses(reviewapp.PendingName)},
		{Kind: application.API, Target: "GET /pending", Uses: uses(reviewapp.PendingName)},
		{Kind: application.HTML, Target: "GET /proposals/{proposal_id}", Uses: uses(reviewapp.ProposalName)},
		{Kind: application.HTML, Target: "GET /proposals/{proposal_id}/card", Uses: uses(reviewapp.ProposalName)},
		{Kind: application.CLI, Target: "proposals list", Uses: uses(reviewapp.ProposalsName)},
		{Kind: application.HTML, Target: "GET /activity", Uses: uses(reviewapp.ActivityName)},
		{Kind: application.HTML, Target: "GET /activity/recent", Uses: uses(reviewapp.ActivityName)},
		{Kind: application.API, Target: "GET /undo", Uses: uses(reviewapp.UndoableName)},
		{Kind: application.API, Target: "GET /effects", Uses: uses(reviewapp.EffectsName)},
		{Kind: application.CLI, Target: "effects list", Uses: uses(reviewapp.EffectsName)},
		{Kind: application.CLI, Target: "writes list", Uses: uses(reviewapp.EffectsName)},
		{Kind: application.API, Target: "GET /effects/{effect_id}", Uses: uses(reviewapp.EffectName)},
		{Kind: application.CLI, Target: "effects show", Uses: uses(reviewapp.EffectName)},
	}
}
