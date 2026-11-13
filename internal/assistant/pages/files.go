package pages

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/web"
)

const (
	uploadSlack = 1 << 20

	badBodyDetail   = "There was an error parsing the body"
	fieldBytes      = 1 << 20
	bodyBufferBytes = 1 << 16
	maxParts        = 1000
	formBytes       = 4 << 20
)

var (
	errBadBody = errors.New("unparseable multipart body")
)

func (p *pages) parseUploads(w http.ResponseWriter, r *http.Request) (url.Values, []files.UploadedFile, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(media, "multipart/") {
		return web.FormValues(r), nil, nil
	}
	limits, err := files.ReadLimits(r.Context(), p.deps.DB.Read)
	check(err)
	r.Body = http.MaxBytesReader(w, r.Body, int64(limits.MessageCap)+uploadSlack)
	boundary := params["boundary"]
	if boundary == "" {
		return url.Values{}, nil, errBadBody
	}
	reader := multipart.NewReader(bufio.NewReaderSize(r.Body, bodyBufferBytes), boundary)
	form := url.Values{}
	var uploads []files.UploadedFile
	var parts, text int
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return form, uploads, nil
		}
		if err != nil {
			return form, nil, uploadReadError(err, limits.MessageCap)
		}
		if parts++; parts > maxParts {
			return form, nil, errBadBody
		}
		name, filename := part.FormName(), part.FileName()
		if filename == "" {
			value, err := io.ReadAll(io.LimitReader(part, fieldBytes+1))
			if err != nil {
				return form, nil, uploadReadError(err, limits.MessageCap)
			}
			if text += len(name) + len(value); len(value) > fieldBytes || text > formBytes {
				return form, nil, errBadBody
			}
			form.Add(name, string(value))
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return form, nil, uploadReadError(err, limits.MessageCap)
		}
		if name == "upload" && len(data) > 0 {
			uploads = append(uploads, files.UploadedFile{Name: filename, Data: data, Type: part.Header.Get("Content-Type")})
		}
	}
}

func uploadReadError(err error, messageCap int) error {
	if errors.As(err, new(*http.MaxBytesError)) {
		return files.MessageCapError(messageCap)
	}
	return errBadBody
}

func (p *pages) command(pattern string, h http.HandlerFunc) http.HandlerFunc {
	command := p.deps.Command(pattern, h)
	return func(w http.ResponseWriter, r *http.Request) {
		limits, err := files.ReadLimits(r.Context(), p.deps.DB.Read)
		check(err)
		r.Body = http.MaxBytesReader(w, r.Body, int64(limits.MessageCap)+uploadSlack)
		command(w, r)
	}
}

func (p *pages) readUploads(w http.ResponseWriter, r *http.Request) (url.Values, []files.UploadedFile, bool) {
	form, uploads, err := p.parseUploads(w, r)
	switch {
	case errors.Is(err, errBadBody):
		p.fail(w, r, http.StatusBadRequest, badBodyDetail)
		return nil, nil, false
	case err != nil && strings.TrimSpace(web.FormString(form, "text", "")) == "":
		p.failure(w, r, "Nothing sent", "Type a message to go with the files.")
		return nil, nil, false
	case err != nil:
		p.failure(w, r, "Not attached", err.Error())
		return nil, nil, false
	}
	return form, uploads, true
}

func (p *pages) fileFailed(w http.ResponseWriter, r *http.Request, err error) bool {
	var e *application.Error
	if errors.As(err, &e) && e.Code == application.NotFound {
		p.fail(w, r, http.StatusNotFound, e.Message)
		return true
	}
	check(err)
	return false
}

func (p *pages) threadFileInput(w http.ResponseWriter, r *http.Request) (asstapp.FileInput, bool) {
	threadID, errs := pathInt(r, "thread_id")
	fileID, more := pathInt(r, "file_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return asstapp.FileInput{}, false
	}
	return asstapp.FileInput{ThreadID: threadID, FileID: fileID}, true
}

func (p *pages) chatFile(w http.ResponseWriter, r *http.Request) {
	in, ok := p.threadFileInput(w, r)
	if !ok {
		return
	}
	read, err := p.assistant().FilePreview(r.Context(), in)
	if p.fileFailed(w, r, err) {
		return
	}
	v := read.Data
	f := v.File
	preview := FilePreview{ID: f.ID, ThreadID: f.ThreadID, RunID: f.RunID, Name: f.Name, MediaType: f.MediaType,
		Bytes: f.Bytes, Origin: f.Origin, CreatedAt: f.CreatedAt, Text: &v.Body, Tokens: f.Tokens,
		ProjectID: f.ProjectID, Render: v.Render, Body: v.Body, Rows: v.Rows, Truncated: v.Truncated}
	p.fragment(w, r, http.StatusOK, "_file.gohtml", FileData{File: preview, FileURL: fmt.Sprintf("/chat/%d/files/%d", in.ThreadID, in.FileID)})
}

func (p *pages) chatFileDownload(w http.ResponseWriter, r *http.Request) {
	if in, ok := p.threadFileInput(w, r); ok {
		p.download(w, r, asstapp.DownloadInput{FileInput: in})
	}
}

func (p *pages) chatFileRaw(w http.ResponseWriter, r *http.Request) {
	if in, ok := p.threadFileInput(w, r); ok {
		p.download(w, r, asstapp.DownloadInput{FileInput: in, Raw: true})
	}
}

func contentType(media string) string {
	if strings.HasPrefix(media, "text/") && !strings.Contains(strings.ToLower(media), "charset=") {
		return media + "; charset=utf-8"
	}
	return media
}

func disposition(name string) string {
	if quoted := url.PathEscape(name); quoted != name {
		return "attachment; filename*=utf-8''" + quoted
	}
	return `attachment; filename="` + name + `"`
}

func (p *pages) download(w http.ResponseWriter, r *http.Request, in asstapp.DownloadInput) {
	read, err := p.assistant().FileDownload(r.Context(), in)
	if p.fileFailed(w, r, err) {
		return
	}
	stream := read.Data
	defer func() { _ = stream.File.Close() }()
	h := w.Header()
	if in.Raw {
		h.Set("Content-Type", stream.MediaType)
	} else {
		h.Set("Content-Type", contentType(stream.MediaType))
		h.Set("Content-Disposition", disposition(stream.Name))
	}
	http.ServeContent(w, r, "", stream.ModTime, stream.File)
}

func (p *pages) projectFiles(w http.ResponseWriter, r *http.Request, projectID int64, status int, problem *string) {
	read, err := p.assistant().FileList(r.Context(), asstapp.FileListInput{ProjectID: projectID})
	if p.fileFailed(w, r, err) {
		return
	}
	p.fragment(w, r, status, "_project_files.gohtml", ProjectFiles{Project: read.Data.Project, Files: read.Data.Files, Error: problem})
}

func (p *pages) projectFilesList(w http.ResponseWriter, r *http.Request) {
	if projectID, ok := pathID(w, r, "project_id"); ok {
		p.projectFiles(w, r, projectID, http.StatusOK, nil)
	}
}

func (p *pages) projectFilesChanged(w http.ResponseWriter, r *http.Request, projectID int64, err error) {
	var e *application.Error
	if errors.As(err, &e) && (e.Code == application.Invalid || e.Code == application.Conflict || e.Code == application.Refused) {
		p.refuseProjectFiles(w, r, projectID, e.Message)
		return
	}
	if p.fileFailed(w, r, err) {
		return
	}
	p.projectFiles(w, r, projectID, http.StatusOK, nil)
}

func (p *pages) projectFilesUpload(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	_, uploads, err := p.parseUploads(w, r)
	if errors.Is(err, errBadBody) {
		p.fail(w, r, http.StatusBadRequest, badBodyDetail)
		return
	}
	if err != nil {
		p.refuseProjectFiles(w, r, projectID, err.Error())
		return
	}
	_, err = p.assistant().ProjectFileUpload(detached(r), asstapp.ProjectUploadInput{ProjectID: projectID, Uploads: uploads})
	p.projectFilesChanged(w, r, projectID, err)
}

func (p *pages) refuseProjectFiles(w http.ResponseWriter, r *http.Request, projectID int64, problem string) {
	if web.IsPartial(r) {
		p.projectFiles(w, r, projectID, http.StatusUnprocessableEntity, &problem)
		return
	}
	p.fail(w, r, http.StatusUnprocessableEntity, problem)
}

func (p *pages) projectFileInput(w http.ResponseWriter, r *http.Request) (asstapp.FileInput, bool) {
	projectID, errs := pathInt(r, "project_id")
	fileID, more := pathInt(r, "file_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return asstapp.FileInput{}, false
	}
	return asstapp.FileInput{ProjectID: projectID, FileID: fileID}, true
}

func (p *pages) projectFileDownload(w http.ResponseWriter, r *http.Request) {
	if in, ok := p.projectFileInput(w, r); ok {
		p.download(w, r, asstapp.DownloadInput{FileInput: in})
	}
}

func (p *pages) projectFileDelete(w http.ResponseWriter, r *http.Request) {
	in, ok := p.projectFileInput(w, r)
	if !ok {
		return
	}
	_, err := p.assistant().ProjectFileDelete(detached(r), in)
	p.projectFilesChanged(w, r, in.ProjectID, err)
}
