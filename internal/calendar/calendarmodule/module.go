package calendarmodule

import (
	"context"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/registry"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

type Module struct{}

func (Module) Register(module.Context) module.Registration {
	command := calendarcmd.Command()
	return module.Registration{
		Name:     module.Calendar,
		Soft:     []string{module.Mail},
		Requires: []module.Requirement{{Name: "calendar credentials", Check: credentials}},
		Commands: []*cli.Command{command},
		Leaves: map[string]module.Leaf{
			"calendar candidates":  {Needs: []string{module.Mail}},
			"calendar google-auth": {Always: true},
		},
		Tools: func(r *registry.Registry) error { return r.Add(calendartools.Tools()...) },
	}
}

func credentials(ctx context.Context, p module.Probe) (string, error) {
	icloud, err := p.Settings.Resolve(ctx, p.Q, calendar.AppleIDKey, 0)
	if err != nil {
		return "", err
	}
	if err := p.Settings.Validate(calendar.AppleIDKey, icloud.Value); err != nil {
		var invalid settings.Invalid
		if !errors.As(err, &invalid) {
			return "", err
		}
		icloud.Value = ""
	}
	password, _ := p.Env("ICLOUD_APP_PASSWORD")
	if strings.TrimSpace(icloud.Value) != "" && strings.TrimSpace(password) != "" {
		return "", nil
	}

	google, err := p.Settings.Resolve(ctx, p.Q, calendar.GoogleClientIDKey, 0)
	if err != nil {
		return "", err
	}
	if err := p.Settings.Validate(calendar.GoogleClientIDKey, google.Value); err != nil {
		var invalid settings.Invalid
		if !errors.As(err, &invalid) {
			return "", err
		}
		google.Value = ""
	}
	secret, _ := p.Env("GOOGLE_OAUTH_CLIENT_SECRET")
	refresh, _ := p.Env("GOOGLE_REFRESH_TOKEN")
	if strings.TrimSpace(google.Value) != "" && strings.TrimSpace(secret) != "" && strings.TrimSpace(refresh) != "" {
		return "", nil
	}
	return "an iCloud CalDAV login or Google Calendar credentials are required", nil
}
