package scheduler

import (
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/runtime"
)

type InvalidJob struct {
	Module string
	Key    string
	Reason string
}

func (e *InvalidJob) Error() string { return "scheduled job " + e.Key + " " + e.Reason }

func Clone(defs []Definition) []Definition {
	out := slices.Clone(defs)
	for i := range out {
		out[i].Resources = slices.Clone(out[i].Resources)
		out[i].Needs = slices.Clone(out[i].Needs)
	}
	return out
}

func Compile(defs []Definition) ([]Definition, error) {
	keys := map[string]bool{}
	names := map[string]bool{}
	settingKeys := map[string]bool{}
	nightly := false
	for _, def := range defs {
		bad := func(reason string) error { return &InvalidJob{Module: def.Module, Key: def.Key, Reason: reason} }
		switch {
		case strings.TrimSpace(def.Module) == "" || def.Key != def.Module+"/"+def.Name:
			return nil, bad("must be keyed by its owner and name")
		case strings.TrimSpace(def.Name) == "" || strings.TrimSpace(def.Name) != def.Name:
			return nil, bad("has an invalid name")
		case keys[def.Key]:
			return nil, bad("is declared twice")
		case names[def.Name]:
			return nil, bad("reuses the name " + def.Name)
		case def.Kind != JobNightly && def.Kind != JobBuiltin:
			return nil, bad("has an unknown kind " + def.Kind)
		case def.Kind == JobNightly && nightly:
			return nil, bad("is a second nightly job")
		case (def.Every > 0) == (def.Schedule != ""):
			return nil, bad("needs exactly one of a fixed interval or a schedule")
		case def.Every < 0 || def.InitialDelay < 0:
			return nil, bad("has a negative interval or initial delay")
		case def.InitialDelay > 0 && def.Every == 0:
			return nil, bad("declares an initial delay without a fixed interval")
		case def.ScheduleSetting != "" && def.ScheduleHost != "":
			return nil, bad("takes its schedule from both a setting and the host")
		case def.Every > 0 && (def.ScheduleSetting != "" || def.ScheduleHost != ""):
			return nil, bad("has a fixed interval and a configurable schedule")
		case def.ScheduleSetting != "" && (settingKeys[def.ScheduleSetting] || def.ScheduleSetting == KeyMinInterval):
			return nil, bad("needs its own schedule setting")
		case def.Overlap:
			return nil, bad("may not overlap itself")
		case def.Shutdown != "" && def.Shutdown != ShutdownDrain && def.Shutdown != ShutdownCancel:
			return nil, bad("has an unknown shutdown policy")
		case def.Backoff != (runtime.Backoff{}) && (def.Backoff.Initial <= 0 || def.Backoff.Maximum < def.Backoff.Initial):
			return nil, bad("needs a positive backoff no larger than its maximum")
		case def.Timeout <= 0:
			return nil, bad("needs a positive timeout")
		case def.Spend > ConditionalSpends:
			return nil, bad("has an unknown spend policy")
		case def.Run == nil:
			return nil, bad("has no run function")
		case def.PromptRun && strings.TrimSpace(def.Prompt) == "":
			return nil, bad("runs a prompt but declares none")
		case slices.ContainsFunc(def.Resources, invalidResource) || slices.Contains(def.Needs, ""):
			return nil, bad("names an invalid resource or an empty module")
		}
		if def.Schedule != "" {
			if err := RecurringSchedule(def.Schedule); err != nil {
				return nil, bad("has an invalid schedule: " + err.Error())
			}
		}
		keys[def.Key], names[def.Name] = true, true
		if def.ScheduleSetting != "" {
			settingKeys[def.ScheduleSetting] = true
		}
		nightly = nightly || def.Kind == JobNightly
	}
	return Clone(defs), nil
}

func invalidResource(key string) bool {
	return !db.ValidResource(key) || strings.HasPrefix(key, "job/")
}
