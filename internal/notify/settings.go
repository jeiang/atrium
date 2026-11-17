package notify

import (
	"context"

	modules "github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/unit"
)

const (
	TuningPerRunCap  = 3
	TuningNightlyCap = 1
)

func Enabled(ctx context.Context) bool {
	return modules.IsEnabled(ctx, unit.Notifications)
}
