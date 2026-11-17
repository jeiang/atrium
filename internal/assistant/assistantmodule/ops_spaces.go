package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

func assistantService(d *web.Deps) *asstapp.Service {
	return asstapp.New(d.DB, d.Env, assistant.Now).WithFiles(d.Cfg.FilesDir()).WithJobs(module.SetFrom(d.Ctx).Catalog().Jobs())
}

func spaceOperations() ([]application.Definition, []application.Binding) {
	return []application.Definition{
			asstapp.DescribeSpaceCreate(), asstapp.DescribeSpaceEdit(), asstapp.DescribeSpaceArchive(),
			asstapp.DescribeSpaceDetail(), asstapp.DescribeSpaceList(), asstapp.DescribeConfiguration(), asstapp.DescribeDefaultsSet(), asstapp.DescribeSubagentRoles(), asstapp.DescribeToolCatalog(),
		}, append(spaceAPIBindings(nil), []application.Binding{
			{Kind: application.HTML, Target: "GET /projects/new", Uses: []application.Name{asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects", Uses: []application.Name{asstapp.SpaceCreateName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "GET /projects/{project_id}/edit", Uses: []application.Name{asstapp.SpaceDetailName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/edit", Uses: []application.Name{asstapp.SpaceEditName, asstapp.SpaceDetailName, asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/archive", Uses: []application.Name{asstapp.SpaceArchiveName}},
			{Kind: application.HTML, Target: "GET /chat/composer-options", Uses: []application.Name{asstapp.ConfigurationName}},
			{Kind: application.HTML, Target: "GET /settings/assistant", Uses: []application.Name{asstapp.ConfigurationName}},
		}...)
}
