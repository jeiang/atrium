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
			asstapp.DescribeProjectFileUpload(), asstapp.DescribeProjectFileDelete(), asstapp.DescribeFileWrite(),
			asstapp.DescribeFilePreview(), asstapp.DescribeFileDownload(), asstapp.DescribeFileList(),
			asstapp.DescribeFileRead(), asstapp.DescribeFileSearch(),
		}, append(fileTransportBindings(nil), []application.Binding{
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/files/{file_id}", Uses: uses(asstapp.FilePreviewName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/files/{file_id}/download", Uses: uses(asstapp.FileDownloadName)},
			{Kind: application.HTML, Target: "GET /chat/{thread_id}/files/{file_id}/raw", Uses: uses(asstapp.FileDownloadName)},
			{Kind: application.HTML, Target: "GET /projects/{project_id}/files", Uses: uses(asstapp.FileListName)},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/files", Uses: uses(asstapp.ProjectFileUploadName, asstapp.FileListName)},
			{Kind: application.HTML, Target: "GET /projects/{project_id}/files/{file_id}/download", Uses: uses(asstapp.FileDownloadName)},
			{Kind: application.HTML, Target: "POST /projects/{project_id}/files/{file_id}/delete", Uses: uses(asstapp.ProjectFileDeleteName, asstapp.FileListName)},
			{Kind: application.HTML, Target: "GET /library/files", Uses: uses(asstapp.FileListName)},
			{Kind: application.Tool, Target: "file_read", Uses: uses(asstapp.FileReadName)},
			{Kind: application.Tool, Target: "file_search", Uses: uses(asstapp.FileSearchName)},
			{Kind: application.Tool, Target: "file_write", Uses: uses(asstapp.FileWriteName)},
		}...)
}
