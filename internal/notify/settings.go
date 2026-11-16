package notify

import (
	"context"

	modules "github.com/jeiang/atrium/internal/module"
)

const (
	TuningPerRunCap  = 3
	TuningNightlyCap = 1
)

func Enabled(ctx context.Context) bool {
	return modules.IsEnabled(ctx, modules.Notifications)
}
