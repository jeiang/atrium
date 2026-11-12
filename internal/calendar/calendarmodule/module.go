package calendarmodule

import (
	"context"
	"errors"
	"slices"
	"strings"

	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	"github.com/jeiang/atrium/internal/attention"
	attentionsources "github.com/jeiang/atrium/internal/attention/sources"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/calendarcmd"
	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/settings"
)

type Module struct{}

func AttentionSources() []attention.Source {
	return []attention.Source{attentionsources.Candidates()}
}

func Requirements() []module.Requirement {
	return []module.Requirement{{Name: "calendar credentials", Check: credentials}}
}

func ToolDefinitions() []module.ToolDefinition {
	return calendartools.Definitions()
}

func (Module) Declare(mc module.Context) module.Declaration {
	command := calendarcmd.Command(mc.Env, mc.Tools)
	return module.Declaration{
		Name:      module.Calendar,
		Settings:  slices.Concat(calendar.SettingsDefs(), calendar.SlotSettings()),
		Host:      calendar.HostSettings(),
		Sections:  calendar.Sections(),
		Attention: AttentionSources(),
		Requires:  Requirements(),
		Commands:  []*cli.Command{command},
		Leaves: map[string]module.Leaf{
			"calendar candidates":  {Needs: []string{module.Mail}},
			"calendar google-auth": {Always: true},
		},
		Tools: ToolDefinitions(),
	}
}

func credentials(ctx context.Context, p module.Probe) (string, error) {
	icloud, err := p.Settings.Resolve(ctx, p.Q, calendar.AppleIDKey)
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
	account, err := p.Settings.Resolve(ctx, p.Q, calendar.AccountKey)
	if err != nil {
		return "", err
	}
	_, icloudErr := calendar.ICloudLogin(ctx, calendar.Settings{AppleID: icloud.Value, Account: strings.TrimSpace(account.Value)})
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
