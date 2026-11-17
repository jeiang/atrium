package assistantmodule

import (
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/tool"
)

func fileToolService(tc *tool.Context) *asstapp.Service {
	return asstapp.New(tc.DB, tc.Env, tc.Now).WithFiles(tc.Cfg.FilesDir()).WithLedger(tc.Ledger)
}

func fileOperations() ([]application.Definition, []application.Binding) {
	uses := func(names ...application.Name) []application.Name { return names }
	return []application.Definition{
			asstapp.DescribeSpaceFileUpload(), asstapp.DescribeSpaceFileDelete(), asstapp.DescribeFileWrite(),
			asstapp.DescribeFilePreview(), asstapp.DescribeFileDownload(), asstapp.DescribeFileList(),
			asstapp.DescribeFileRead(), asstapp.DescribeFileSearch(),
		}, append(fileAPIBindings(nil), []application.Binding{
			{Kind: application.Tool, Target: "file_read", Uses: uses(asstapp.FileReadName)},
			{Kind: application.Tool, Target: "file_search", Uses: uses(asstapp.FileSearchName)},
			{Kind: application.Tool, Target: "file_write", Uses: uses(asstapp.FileWriteName)},
		}...)
}
