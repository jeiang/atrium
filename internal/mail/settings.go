package mail

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/endpoint"
	"github.com/jeiang/atrium/internal/numx"
	"github.com/jeiang/atrium/internal/settings"
)

const module = "mail"

const (
	KeyMode                   = "mail_mode"
	KeyWriteCap               = "write_cap"
	KeyUncertainDest          = "uncertain_dest"
	KeyLoopsWaitingAfterDays  = "openloops_waiting_after_days"
	KeyLoopsRemindAheadDays   = "openloops_remind_ahead_days"
	KeyLoopsRemindHour        = "openloops_remind_hour"
	KeyLoopsSnoozeDefaultDays = "openloops_snooze_default_days"
	KeyWatchesCarrierDomains  = "watches_carrier_domains"
)

const modeDefault = config.Shadow

func init() {
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check})
	}
	ui := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.UI, settings.Def{Key: key, Module: module, Type: kind, Default: def, Check: check})
	}
	settings.MustRegisterHost(
		settings.HostDef{Key: endpoint.KeyChatURL, Module: module, Type: settings.String},
		settings.HostDef{Key: endpoint.KeyChatModel, Module: module, Type: settings.String, Default: "qwen", Check: nonBlank},
	)
	settings.MustRegister(
		settings.Deploy(settings.Def{Key: KeyMode, Module: module, Type: settings.String, Default: modeDefault, Check: modeCheck}),
		settings.Value(settings.UI, settings.Def{
			Key: KeyWriteCap, Label: "Automatic write cap", Module: module, Type: settings.Int, Default: "100",
			Check: settings.Between(1, 1000)}),
		ui(KeyUncertainDest, settings.String, "Misc", nonBlank),
		settings.Shown("Waiting on them after (days)", adv(KeyLoopsWaitingAfterDays, settings.Int, "3", atLeast(1))),
		settings.Shown("Remind ahead of a due date (days)", adv(KeyLoopsRemindAheadDays, settings.Int, "1", atLeast(1))),
		ui(KeyLoopsRemindHour, settings.Int, "9", settings.Between(0, 23)),
		settings.Shown("Default snooze (days)", adv(KeyLoopsSnoozeDefaultDays, settings.Int, "3", atLeast(1))),
		ui(KeyWatchesCarrierDomains, settings.String, "ups.com,fedex.com,usps.com,dhl.com,usps.gov,dhl.de", domainsCheck),
	)
}

func atLeast(lo int64) func(string) error {
	return func(value string) error {
		n, ok := numx.ParseInt(value)
		if !ok {
			return settings.Invalid("must be a whole number")
		}
		if n < lo {
			return settings.Invalid("must be at least " + strconv.FormatInt(lo, 10))
		}
		return nil
	}
}

func nonBlank(value string) error {
	if strings.TrimSpace(value) == "" {
		return settings.Invalid("must not be blank")
	}
	return nil
}

func modeCheck(value string) error {
	for _, m := range config.Modes {
		if value == m {
			return nil
		}
	}
	return settings.Invalid(fmt.Sprintf("must be one of (%s, %s), got %s", strconv.Quote(config.Shadow), strconv.Quote(config.Live), strconv.Quote(value)))
}

func domainsCheck(value string) error {
	if len(endpoint.SplitList(value)) == 0 {
		return settings.Invalid("must list at least one domain")
	}
	for _, d := range endpoint.SplitList(value) {
		for _, c := range d {
			if c != '.' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				return settings.Invalid("domains are lowercase letters, digits, dots and hyphens, got " + strconv.Quote(d))
			}
		}
	}
	return nil
}

func WallClock() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func Mode(ctx context.Context, q db.Querier) (string, error) {
	r := endpoint.Read(ctx, q)
	value := r.Text(KeyMode)
	if err := r.Err(); err != nil {
		return "", err
	}
	if err := modeCheck(value); err != nil {
		return "", fmt.Errorf("%s %w", KeyMode, err)
	}
	return value, nil
}

func UncertainDest(ctx context.Context, q db.Querier, account string) (string, error) {
	spec, ok := config.From(ctx).Account(account)
	if !ok || spec.UncertainDest() == "" {
		return "", nil
	}
	r := endpoint.Read(ctx, q)
	return r.Text(KeyUncertainDest), r.Err()
}
