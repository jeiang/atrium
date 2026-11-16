package assistantmodule

import (
	"context"
	"mime/multipart"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/web"
)

type apiSpaceUpload struct {
	SpaceID int64                `path:"space_id" api:"min=1" json:"-"`
	Uploads []files.UploadedFile `json:"-"`
}

func (in *apiSpaceUpload) DecodeMultipart(reader *multipart.Reader) error {
	uploads, err := decodeUploads(reader, in)
	in.Uploads = uploads
	return err
}

type apiSpaceFile struct {
	SpaceID int64 `path:"space_id" api:"min=1" json:"-"`
	FileID  int64 `path:"file_id" api:"min=1" json:"-"`
}
type apiThreadFile struct {
	ThreadID int64 `path:"thread_id" api:"min=1" json:"-"`
	FileID   int64 `path:"file_id" api:"min=1" json:"-"`
}
type apiSpaceFiles struct {
	SpaceID int64      `path:"space_id" api:"min=1" json:"-"`
	Limit   api.Limit  `query:"limit" json:"-"`
	Cursor  api.Cursor `query:"cursor" json:"-"`
}
type apiThreadFiles struct {
	ThreadID int64      `path:"thread_id" api:"min=1" json:"-"`
	Limit    api.Limit  `query:"limit" json:"-"`
	Cursor   api.Cursor `query:"cursor" json:"-"`
}
type apiLibraryFiles struct {
	Limit  api.Limit  `query:"limit" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
}

type libraryCursor struct {
	At float64 `json:"at"`
	ID int64   `json:"id"`
}

func (c libraryCursor) Valid() bool { return c.ID > 0 && c.At > 0 }

func libraryPage(ctx context.Context, svc *asstapp.Service, limit api.Limit, cursor api.Cursor) (application.Read[web.List[asstapp.LibraryEntry]], error) {
	in := asstapp.FileListInput{Limit: int(limit) + 1}
	if cursor.Set() {
		var last libraryCursor
		if err := cursor.Last(&last); err != nil {
			return application.Read[web.List[asstapp.LibraryEntry]]{}, err
		}
		in.After, in.AfterAt = last.ID, last.At
	}
	read, err := svc.FileList(ctx, in)
	if err != nil {
		return application.Read[web.List[asstapp.LibraryEntry]]{}, err
	}
	rows := read.Data.Library
	page := web.List[asstapp.LibraryEntry]{}
	if len(rows) > int(limit) {
		rows = rows[:int(limit)]
		end := rows[len(rows)-1]
		page.NextCursor = cursor.Next(libraryCursor{At: end.CreatedAt, ID: end.ID})
	}
	page.Items = append([]asstapp.LibraryEntry{}, rows...)
	return application.Read[web.List[asstapp.LibraryEntry]]{Revision: read.Revision, Data: page}, nil
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
	bodyless.Area = "knowledge"
	bodyless.Body = application.BodyNone
	bodyless.Success = 204
	threadList, threadPreview, library, spaceList, spacePreview := shape, shape, shape, shape, shape
	library.Errors = errs(application.Invalid)
	library.ID = "assistant.file.library"
	threadList.ID = "assistant.file.thread.list"
	threadPreview.ID = "assistant.file.thread.preview"
	spaceList.Area, spaceList.ID = "knowledge", "knowledge.space.files"
	spacePreview.Area, spacePreview.ID = "knowledge", "knowledge.space.file.preview"
	out := []application.Binding{
		apiCommand(p, asstapp.DescribeSpaceFileUpload(), multipartShape("knowledge", 201, application.Invalid, application.NotFound, application.Conflict), "POST /knowledge/spaces/{space_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiSpaceUpload) (application.Change[asstapp.FileChanged], error) {
			return svc.SpaceFileUpload(ctx, asstapp.SpaceUploadInput{ProjectID: in.SpaceID, Uploads: in.Uploads})
		}),
		apiCommand(p, asstapp.DescribeSpaceFileDelete(), bodyless, "DELETE /knowledge/spaces/{space_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiSpaceFile) (application.Change[asstapp.FileChanged], error) {
			return svc.SpaceFileDelete(ctx, asstapp.FileInput{ProjectID: in.SpaceID, FileID: in.FileID})
		}),
		apiQuery(p, asstapp.DescribeFileList(), threadList, "GET /threads/{thread_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiThreadFiles) (application.Read[web.List[files.Summary]], error) {
			return filePage(ctx, svc, asstapp.FileListInput{ThreadID: in.ThreadID}, in.Limit, in.Cursor)
		}),
		apiQuery(p, asstapp.DescribeFileList(), spaceList, "GET /knowledge/spaces/{space_id}/files", func(ctx context.Context, svc *asstapp.Service, in apiSpaceFiles) (application.Read[web.List[files.Summary]], error) {
			return filePage(ctx, svc, asstapp.FileListInput{ProjectID: in.SpaceID}, in.Limit, in.Cursor)
		}),
		apiQuery(p, asstapp.DescribeFileList(), library, "GET /files", func(ctx context.Context, svc *asstapp.Service, in apiLibraryFiles) (application.Read[web.List[asstapp.LibraryEntry]], error) {
			return libraryPage(ctx, svc, in.Limit, in.Cursor)
		}),
		apiQuery(p, asstapp.DescribeFilePreview(), threadPreview, "GET /threads/{thread_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiThreadFile) (application.Read[asstapp.FileView], error) {
			return svc.FilePreview(ctx, asstapp.FileInput{ThreadID: in.ThreadID, FileID: in.FileID})
		}),
		apiQuery(p, asstapp.DescribeFilePreview(), spacePreview, "GET /knowledge/spaces/{space_id}/files/{file_id}", func(ctx context.Context, svc *asstapp.Service, in apiSpaceFile) (application.Read[asstapp.FileView], error) {
			return svc.FilePreview(ctx, asstapp.FileInput{ProjectID: in.SpaceID, FileID: in.FileID})
		}),
	}
	for _, method := range []string{"GET", "HEAD"} {
		binary := shape
		binary.Body = application.BodyBinary
		spaceBinary, threadBinary := binary, binary
		spaceBinary.Area, spaceBinary.ID = "knowledge", "knowledge.space.file.download."+strings.ToLower(method)
		threadBinary.ID = "assistant.file.thread.download." + strings.ToLower(method)
		out = append(out,
			apiQuery(p, asstapp.DescribeFileDownload(), threadBinary, method+" /threads/{thread_id}/files/{file_id}/content", func(ctx context.Context, svc *asstapp.Service, in apiThreadFile) (application.Read[api.Binary], error) {
				return downloadFile(ctx, svc, asstapp.FileInput{ThreadID: in.ThreadID, FileID: in.FileID})
			}),
			apiQuery(p, asstapp.DescribeFileDownload(), spaceBinary, method+" /knowledge/spaces/{space_id}/files/{file_id}/content", func(ctx context.Context, svc *asstapp.Service, in apiSpaceFile) (application.Read[api.Binary], error) {
				return downloadFile(ctx, svc, asstapp.FileInput{ProjectID: in.SpaceID, FileID: in.FileID})
			}),
		)
	}
	return out
}
