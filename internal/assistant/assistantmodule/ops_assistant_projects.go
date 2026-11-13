package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/web"
)

func assistantService(d *web.Deps) *asstapp.Service {
	return asstapp.New(d.DB, d.Env, assistant.Now)
}

func projectOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			asstapp.DescribeProjectCreate(), asstapp.DescribeProjectEdit(), asstapp.DescribeProjectArchive(),
			asstapp.DescribeProjectDetail(), asstapp.DescribeProjectList(), asstapp.DescribeConfiguration(),
		}, []application.Binding{
			{Kind: application.HTML, Target: "GET /projects/new", Uses: []application.Name{asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects", Uses: []application.Name{asstapp.ProjectCreateName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "GET /projects/{project_id}/edit", Uses: []application.Name{asstapp.ProjectDetailName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/edit", Uses: []application.Name{asstapp.ProjectEditName, asstapp.ProjectDetailName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/archive", Uses: []application.Name{asstapp.ProjectArchiveName}},
			{Kind: application.HTML, Target: "GET /chat/composer-options", Uses: []application.Name{asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "GET /settings/assistant", Uses: []application.Name{asstapp.ConfigurationName}},
		}
}
