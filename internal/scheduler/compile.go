package scheduler

import (
	"slices"
	"strings"
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
		case def.ScheduleSetting == "" || settingKeys[def.ScheduleSetting] || def.ScheduleSetting == KeyMinInterval:
			return nil, bad("needs its own schedule setting")
		case def.Timeout <= 0:
			return nil, bad("needs a positive timeout")
		case def.Spend > ConditionalSpends:
			return nil, bad("has an unknown spend policy")
		case def.Run == nil:
			return nil, bad("has no run function")
		case def.PromptRun && strings.TrimSpace(def.Prompt) == "":
			return nil, bad("runs a prompt but declares none")
		case slices.Contains(def.Resources, "") || slices.Contains(def.Needs, ""):
			return nil, bad("names an empty resource or module")
		}
		spec, err := Parse(def.Schedule, "UTC")
		if err != nil {
			return nil, bad("has an invalid schedule: " + err.Error())
		}
		if !spec.Recurring() {
			return nil, bad("must have a recurring schedule")
		}
		keys[def.Key], names[def.Name], settingKeys[def.ScheduleSetting] = true, true, true
		nightly = nightly || def.Kind == JobNightly
	}
	return Clone(defs), nil
}
