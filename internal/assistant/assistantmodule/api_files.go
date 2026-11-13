package assistantmodule

import (
	"context"
	"mime/multipart"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
)

type apiProjectUpload struct {
	ProjectID int64                `path:"project_id" json:"-"`
	Uploads   []files.UploadedFile `json:"-"`
}

func (in *apiProjectUpload) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}

type apiProjectFile struct {
	ProjectID int64 `path:"project_id" json:"-"`
	FileID    int64 `path:"file_id" json:"-"`
}

func fileTransportBindings(p *apiPages) []application.Binding {
	return []application.Binding{
		apiCommand(p, asstapp.DescribeProjectFileUpload(), multipartShape("projects", 201, application.Invalid, application.NotFound, application.Conflict), "POST /projects/{project_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiProjectUpload) (application.Change[asstapp.FileChanged], error) {
			return svc.ProjectFileUpload(ctx, asstapp.ProjectUploadInput{ProjectID: in.ProjectID, Uploads: in.Uploads})
		}),
		apiQuery(p, asstapp.DescribeFileDownload(), application.APIShape{Area: "files", Body: application.BodyBinary, Errors: errs(application.NotFound)}, "GET /projects/{project_id}/files/{file_id}/content", func(ctx context.Context, svc *asstapp.Service, in apiProjectFile) (application.Read[api.Binary], error) {
			read, err := svc.FileDownload(ctx, asstapp.DownloadInput{FileInput: asstapp.FileInput{ProjectID: in.ProjectID, FileID: in.FileID}})
			return readAs(read, err, func(v asstapp.FileStream) api.Binary {
				return api.Binary{Name: v.Name, MediaType: v.MediaType, ModTime: v.ModTime, File: v.File}
			})
		}),
	}
}
