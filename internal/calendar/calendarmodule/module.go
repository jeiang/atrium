package calendarmodule

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/jeiang/atrium/internal/assistant/jobworker"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/calendar"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/connections"
	connectionsapp "github.com/jeiang/atrium/internal/connections/application"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/passrun"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/scheduler/jobcmd"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/unit"
	"github.com/jeiang/atrium/internal/web"
)

type Module struct {
	Jobs jobworker.Dependencies
}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Candidates()}
}

func Requirements() []module.Requirement {
	return []module.Requirement{{Name: "calendar credentials", Check: credentials}}
}

func ToolDefinitions() []module.ToolDefinition {
	return calendartools.Definitions()
}

func (m Module) Declare(mc module.Context) module.Declaration {
	command := jobcmd.DelegatingSubs(calendarcmd.Command(m.calendarCLI(mc.Env, mc.Tools), connectionsapp.CLI(mc.Env)), map[string]string{"sync": unit.Calendar + "/" + passrun.CalDAV, "candidates": unit.Calendar + "/" + calendar.JobCandidates})
	operations, bindings := m.calendarOperations()
	return module.Declaration{
		Name:  unit.Calendar,
		Title: "Calendar",
		App:   true,
		Needs: []module.Need{
			{Kind: davmirror.KindICloud, Soft: true, Capability: davmirror.CapabilityCalDAV, Features: []string{"iCloud calendar sync"}},
			{Kind: connections.KindGoogle, Soft: true, Capability: connections.GoogleCalendar, Features: []string{"Google calendar sync"}},
		},
		Packages:      []string{"internal/assistant/tools/calendar", "internal/calendar"},
		Tables:        []string{"agent_events", "event_candidates"},
		EffectOrigins: []module.EffectOrigin{{Name: "calendar", Direct: true}},
		Refs:          []module.RefKind{eventRef(mc.Env)},
		Day:           dayProvider{env: mc.Env},
		Settings:      slices.Concat(calendar.SettingsDefs(), calendar.SlotSettings()),
		Host:          calendar.HostSettings(),
		Sections:      calendar.Sections(),
		Attention:     AttentionSources(),
		Jobs:          []scheduler.Definition{CandidatesJob(m.Jobs), CalDAVJob(m.Jobs)},
		Requires:      Requirements(),
		Commands:      []*cli.Command{command},
		Leaves: map[string]module.Leaf{
			"calendar candidates":  {Needs: []string{unit.Mail}},
			"calendar google-auth": {Always: true},
		},
		Tools: ToolDefinitions(),
		Effects: func(d module.EffectDeps) module.Effects {
			p := &calendar.Proposals{DB: d.DB, Ledger: d.Ledger, Now: d.Now, Env: d.Env}
			return module.Effects{
				Proposals: p.Proposals(unit.Calendar),
				Undo:      slices.Concat(p.Undo(), calapp.Undo(d.Ledger, d.Env)),
			}
		},
		Operations: operations,
		Bindings:   bindings,
		Pages:      []web.Page{{Module: func(_ chi.Router, d *web.Deps) { m.calendarRoutes(d) }}},
	}
}

func credentials(ctx context.Context, p module.Probe) (string, error) {
	icloud, err := p.Settings.Resolve(ctx, p.Q, davmirror.AppleIDKey)
	if err != nil {
		return "", err
	}
	if err := p.Settings.Validate(davmirror.AppleIDKey, icloud.Value); err != nil {
		var invalid settings.Invalid
		if !errors.As(err, &invalid) {
			return "", err
		}
		icloud.Value = ""
	}
	account, err := p.Settings.Resolve(ctx, p.Q, davmirror.AccountKey)
	if err != nil {
		return "", err
	}
	_, icloudErr := davmirror.ICloudLogin(ctx, davmirror.Login{AppleID: icloud.Value, Account: strings.TrimSpace(account.Value)})
	if icloudErr == nil {
		return "", nil
	}

	set, err := calendar.LoadSettings(ctx, p.Q)
	if err != nil {
		return "", err
	}
	if set.Google != nil {
		clientID, _ := set.Google.Endpoint("client_id")
		secret, _ := set.Google.Credential("oauth/client_secret")
		grant, _ := set.Google.Credential("oauth/calendar/refresh")
		if strings.TrimSpace(clientID) != "" && secret != "" && grant != "" {
			return "", nil
		}
	}
	return "an iCloud CalDAV login or Google Calendar credentials are required (" + icloudErr.Error() + ")", nil
}
