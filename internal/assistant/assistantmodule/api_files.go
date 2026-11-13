package assistantmodule

import (
	"context"
	"mime/multipart"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/web"
)

type apiProjectUpload struct {
	ProjectID int64                `path:"project_id" api:"min=1" json:"-"`
	Uploads   []files.UploadedFile `json:"-"`
}

func (in *apiProjectUpload) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}

type apiProjectFile struct {
	ProjectID int64 `path:"project_id" api:"min=1" json:"-"`
	FileID    int64 `path:"file_id" api:"min=1" json:"-"`
}
type apiThreadFile struct {
	ThreadID int64 `path:"thread_id" api:"min=1" json:"-"`
	FileID   int64 `path:"file_id" api:"min=1" json:"-"`
}
type apiProjectFiles struct {
	ProjectID int64      `path:"project_id" api:"min=1" json:"-"`
	Limit     api.Limit  `query:"limit" json:"-"`
	Cursor    api.Cursor `query:"cursor" json:"-"`
}
type apiThreadFiles struct {
	ThreadID int64      `path:"thread_id" api:"min=1" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
}

func filePage(ctx context.Context, svc *asstapp.Service, in asstapp.FileListInput, limit api.Limit, cursor api.Cursor) (application.Read[web.List[files.Summary]], error) {
	after, err := cursorAfter(cursor)
	if err != nil {
		return application.Read[web.List[files.Summary]]{}, err
	}
	in.After, in.Limit = after, int(limit)+1
	read, err := svc.FileList(ctx, in)
	if err != nil {
		return application.Read[web.List[files.Summary]]{}, err
	}
	rows := read.Data.Files
	page := web.List[files.Summary]{}
	if len(rows) > int(limit) {
		rows = rows[:int(limit)]
		page.NextCursor = cursor.Next(rows[len(rows)-1].ID)
	}
	page.Items = files.Summaries(rows)
	return application.Read[web.List[files.Summary]]{Revision: read.Revision, Data: page}, nil
}
func downloadFile(ctx context.Context, svc *asstapp.Service, in asstapp.FileInput) (application.Read[api.Binary], error) {
	read, err := svc.FileDownload(ctx, asstapp.DownloadInput{FileInput: in})
	return readAs(read, err, func(v asstapp.FileStream) api.Binary {
		return api.Binary{Name: v.Name, MediaType: v.MediaType, ModTime: v.ModTime, File: v.File}
	})
}
func fileAPIBindings(p *apiPages) []application.Binding {
	shape := application.APIShape{Area: "files", Errors: errs(application.NotFound, application.Invalid)}
	bodyless := shape
	bodyless.Area = "projects"
	bodyless.Body = application.BodyNone
	bodyless.Success = 204
	out := []application.Binding{
		apiCommand(p, asstapp.DescribeProjectFileUpload(), multipartShape("projects", 201, application.Invalid, application.NotFound, application.Conflict), "POST /projects/{project_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiProjectUpload) (application.Change[asstapp.FileChanged], error) {
			return svc.ProjectFileUpload(ctx, asstapp.ProjectUploadInput{ProjectID: in.ProjectID, Uploads: in.Uploads})
		}),
		apiCommand(p, asstapp.DescribeProjectFileDelete(), bodyless, "DELETE /projects/{project_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiProjectFile) (application.Change[asstapp.FileChanged], error) {
			return svc.ProjectFileDelete(ctx, asstapp.FileInput{ProjectID: in.ProjectID, FileID: in.FileID})
		}),
		apiQuery(p, asstapp.DescribeFileList(), shape, "GET /projects/{project_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiProjectFiles) (application.Read[web.List[files.Summary]], error) {
			return filePage(ctx, svc, asstapp.FileListInput{ProjectID: in.ProjectID}, in.Limit, in.Cursor)
		}),
		apiQuery(p, asstapp.DescribeFileList(), shape, "GET /threads/{thread_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiThreadFiles) (application.Read[web.List[files.Summary]], error) {
			return filePage(ctx, svc, asstapp.FileListInput{ThreadID: in.ThreadID}, in.Limit, in.Cursor)
		}),
		apiQuery(p, asstapp.DescribeFilePreview(), shape, "GET /projects/{project_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiProjectFile) (application.Read[asstapp.FileView], error) {
			return svc.FilePreview(ctx, asstapp.FileInput{ProjectID: in.ProjectID, FileID: in.FileID})
		}),
		apiQuery(p, asstapp.DescribeFilePreview(), shape, "GET /threads/{thread_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiThreadFile) (application.Read[asstapp.FileView], error) {
			return svc.FilePreview(ctx, asstapp.FileInput{ThreadID: in.ThreadID, FileID: in.FileID})
		}),
	}
	for _, method := range []string{"GET", "HEAD"} {
		binary := shape
		binary.Body = application.BodyBinary
		out = append(out,
			apiQuery(p, asstapp.DescribeFileDownload(), binary, method+" /projects/{project_id}/files/{file_id}/content", func(ctx context.Context, svc *asstapp.Service, in apiProjectFile) (application.Read[api.Binary], error) {
				return downloadFile(ctx, svc, asstapp.FileInput{ProjectID: in.ProjectID, FileID: in.FileID})
			}),
			apiQuery(p, asstapp.DescribeFileDownload(), binary, method+" /threads/{thread_id}/files/{file_id}/content", func(ctx context.Context, svc *asstapp.Service, in apiThreadFile) (application.Read[api.Binary], error) {
				return downloadFile(ctx, svc, asstapp.FileInput{ThreadID: in.ThreadID, FileID: in.FileID})
			}),
		)
	}
	return out
}
