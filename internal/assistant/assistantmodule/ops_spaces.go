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
	}, spaceAPIBindings(nil)
}
