package settingspage

import (
	"context"

	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

type Group struct {
	settingsapp.Group
	URL string
}

type Module struct {
	Name string
	URL  string
}

type Control struct {
	Modules []Module
	Mark    func(name string) string
	State   func(ctx context.Context, q settingsapp.Querier, name string) (settingsapp.ModuleView, error)
}
