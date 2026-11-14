package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
)

func researchOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeResearch(), asstapp.DescribeLookup(), asstapp.DescribeSearchWeb(),
			asstapp.DescribeWebFetch(), asstapp.DescribeWeather(),
			asstapp.DescribePropose(),
		}, []application.Binding{
			{Kind: application.Tool, Target: "research", Uses: uses(asstapp.ResearchName)},
			{Kind: application.Tool, Target: "lookup", Uses: uses(asstapp.LookupName)},
			{Kind: application.Tool, Target: "search_web", Uses: uses(asstapp.SearchWebName)},
			{Kind: application.Tool, Target: "web_fetch", Uses: uses(asstapp.WebFetchName)},
			{Kind: application.Tool, Target: "weather", Uses: uses(asstapp.WeatherName)},
			{Kind: application.Tool, Target: "propose", Uses: uses(asstapp.ProposeName)},
		}
}
