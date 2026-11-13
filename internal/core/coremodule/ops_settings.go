package coremodule

import (
	"github.com/jeiang/atrium/internal/application"
	settingsapp "github.com/jeiang/atrium/internal/settings/application"
)

func settingsOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return []application.Definition{
			settingsapp.DescribeRead(),
			settingsapp.DescribeSet(),
			settingsapp.DescribeUnset(),
			settingsapp.DescribeSecretStatus(),
			settingsapp.DescribeSecretSet(),
			settingsapp.DescribeSecretDelete(),
		}, []application.Binding{
			{Kind: application.CLI, Target: "settings list", Uses: uses(settingsapp.ReadName)},
			{Kind: application.CLI, Target: "settings get", Uses: uses(settingsapp.ReadName)},
			{Kind: application.CLI, Target: "settings set", Uses: []application.Name{settingsapp.SetName, settingsapp.ReadName}},
			{Kind: application.CLI, Target: "settings unset", Uses: []application.Name{settingsapp.UnsetName, settingsapp.ReadName}},
			{Kind: application.CLI, Target: "secrets set", Uses: uses(settingsapp.SecretSetName)},
			{Kind: application.CLI, Target: "secrets delete", Uses: uses(settingsapp.SecretDeleteName)},
			{Kind: application.CLI, Target: "secrets status", Uses: uses(settingsapp.SecretStatusName)},
			{Kind: application.HTML, Target: "POST /settings/groups/{module}/{name}", Uses: []application.Name{settingsapp.SetName, settingsapp.ReadName}},
			{Kind: application.HTML, Target: "POST /settings/sections/{name}", Uses: []application.Name{settingsapp.SetName}},
		}
}
