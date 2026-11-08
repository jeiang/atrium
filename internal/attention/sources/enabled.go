package sources

import (
	"context"

	"github.com/jeiang/atrium/internal/attention"
	"github.com/jeiang/atrium/internal/module"
)

func Enabled(ctx context.Context, enabled func(context.Context, string) bool) []attention.Source {
	var out []attention.Source
	if enabled(ctx, module.Core) {
		out = append(out, Proposals())
	}
	if enabled(ctx, module.Mail) {
		out = append(out, Drafts(), Loops(), Commitments(), Packages())
	}
	if enabled(ctx, module.Calendar) {
		out = append(out, Candidates())
	}
	if enabled(ctx, module.Assistant) {
		out = append(out, Plans())
	}
	return out
}
