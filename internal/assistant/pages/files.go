package pages

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	mailmime "github.com/jeiang/atrium/internal/mail/mime"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	uploadSlack = 1 << 20
	sniffBytes  = 4096

	badBodyDetail   = "There was an error parsing the body"
	fieldBytes      = 1 << 20
	bodyBufferBytes = 1 << 16
	maxParts        = 1000
	formBytes       = 4 << 20
)

var (
	errBadBody     = errors.New("unparseable multipart body")
	markdownTypes  = []string{"text/markdown", "text/x-markdown"}
	tableTypes     = []string{"text/csv", "application/csv", files.XLSXType}
	oversizeUpload = files.FileError(fmt.Sprintf("files in one message must total under %d MB", files.MessageCapBytes>>20))
)

func (p *pages) parseUploads(w http.ResponseWriter, r *http.Request) (url.Values, []files.UploadedFile, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(media, "multipart/") {
		return web.FormValues(r), nil, nil
	}
	r.Body = http.MaxBytesReader(w, r.Body, files.MessageCapBytes+uploadSlack)
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
			return form, nil, uploadReadError(err)
		}
		if parts++; parts > maxParts {
			return form, nil, errBadBody
		}
		name, filename := part.FormName(), part.FileName()
		if filename == "" {
			value, err := io.ReadAll(io.LimitReader(part, fieldBytes+1))
			if err != nil {
				return form, nil, uploadReadError(err)
			}
			if text += len(name) + len(value); len(value) > fieldBytes || text > formBytes {
				return form, nil, errBadBody
			}
			form.Add(name, string(value))
			continue
		}
		data, err := io.ReadAll(part)
		if err != nil {
			return form, nil, uploadReadError(err)
		}
		if name == "upload" && len(data) > 0 {
			uploads = append(uploads, files.UploadedFile{Name: filename, Data: data, Type: part.Header.Get("Content-Type")})
		}
	}
}

func uploadReadError(err error) error {
	if errors.As(err, new(*http.MaxBytesError)) {
		return oversizeUpload
	}
	return errBadBody
}

func (p *pages) readUploads(w http.ResponseWriter, r *http.Request) (url.Values, []files.UploadedFile, bool) {
	form, uploads, err := p.parseUploads(w, r)
	switch {
	case errors.Is(err, errBadBody):
		p.fail(w, r, http.StatusBadRequest, badBodyDetail)
		return nil, nil, false
	case err != nil && py.Strip(web.FormString(form, "text", "")) == "":
		p.failure(w, r, "Nothing sent", "Type a message to go with the files.")
		return nil, nil, false
	case err != nil:
		p.failure(w, r, "Not attached", err.Error())
		return nil, nil, false
	}
	return form, uploads, true
}

func archivedProject(project *projects.Project, verb string) (string, bool) {
	if !bool(project.Archived) {
		return "", false
	}
	return fmt.Sprintf("“%s” is archived. Restore it to %s.", project.Name, verb), true
}

func (p *pages) remote() files.RemoteDeleter {
	if p.svc.Client != nil {
		return p.svc.Client
	}
	if client, err := anthropic.New(p.deps.Env); err == nil {
		return client
	}
	return nil
}

func (p *pages) threadFile(w http.ResponseWriter, r *http.Request) (*files.Row, string, bool) {
	threadID, errs := pathInt(r, "thread_id")
	fileID, more := pathInt(r, "file_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return nil, "", false
	}
	thread, err := threads.Get(r.Context(), p.deps.DB.Read, threadID)
	check(err)
	if thread == nil {
		p.fail(w, r, http.StatusNotFound, "unknown thread")
		return nil, "", false
	}
	found, err := files.Get(r.Context(), p.deps.DB.Read, fileID)
	check(err)
	if found == nil || found.ThreadID == nil || *found.ThreadID != threadID {
		p.fail(w, r, http.StatusNotFound, "unknown file")
		return nil, "", false
	}
	return found, fmt.Sprintf("/chat/%d/files/%d", threadID, fileID), true
}

func (p *pages) chatFile(w http.ResponseWriter, r *http.Request) {
	found, fileURL, ok := p.threadFile(w, r)
	if !ok {
		return
	}
	preview, err := filePreview(r.Context(), p.deps.DB.Read, *found)
	check(err)
	p.fragment(w, r, http.StatusOK, "_file.gohtml", map[string]any{"file": preview, "file_url": fileURL})
}

func (p *pages) chatFileDownload(w http.ResponseWriter, r *http.Request) {
	if found, _, ok := p.threadFile(w, r); ok {
		p.download(w, r, *found)
	}
}

func (p *pages) chatFileRaw(w http.ResponseWriter, r *http.Request) {
	found, _, ok := p.threadFile(w, r)
	if !ok {
		return
	}
	if !files.IsImageType(found.MediaType) {
		p.fail(w, r, http.StatusNotFound, "not an image")
		return
	}
	f, err := os.Open(found.Path)
	if err != nil {
		p.fail(w, r, http.StatusNotFound, "file is missing from storage")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		p.fail(w, r, http.StatusNotFound, "file is missing from storage")
		return
	}
	w.Header().Set("Content-Type", found.MediaType)
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func contentType(media string) string {
	if strings.HasPrefix(media, "text/") && !strings.Contains(py.Lower(media), "charset=") {
		return media + "; charset=utf-8"
	}
	return media
}

func disposition(name string) string {
	if quoted := py.URLQuote(name); quoted != name {
		return "attachment; filename*=utf-8''" + quoted
	}
	return `attachment; filename="` + name + `"`
}

func (p *pages) download(w http.ResponseWriter, r *http.Request, found files.Row) {
	f, err := os.Open(found.Path)
	if err != nil {
		p.fail(w, r, http.StatusNotFound, "file is missing from storage")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		p.fail(w, r, http.StatusNotFound, "file is missing from storage")
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType(found.MediaType))
	h.Set("Content-Disposition", disposition(found.Name))
	http.ServeContent(w, r, "", info.ModTime(), f)
}

func (p *pages) loadProject(w http.ResponseWriter, r *http.Request, projectID int64) (*projects.Project, bool) {
	found, err := projects.Get(r.Context(), p.deps.DB.Read, projectID)
	check(err)
	if found == nil {
		p.fail(w, r, http.StatusNotFound, "unknown project")
		return nil, false
	}
	return found, true
}

func (p *pages) projectFiles(w http.ResponseWriter, r *http.Request, projectID int64, status int, problem *string) {
	project, ok := p.loadProject(w, r, projectID)
	if !ok {
		return
	}
	rows, err := files.OfProject(r.Context(), p.deps.DB.Read, projectID)
	check(err)
	if rows == nil {
		rows = []files.Row{}
	}
	p.fragment(w, r, status, "_project_files.gohtml", map[string]any{"project": project, "files": rows, "error": problem})
}

func (p *pages) projectFilesList(w http.ResponseWriter, r *http.Request) {
	if projectID, ok := pathID(w, r, "project_id"); ok {
		p.projectFiles(w, r, projectID, http.StatusOK, nil)
	}
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
	project, ok := p.loadProject(w, r, projectID)
	if !ok {
		return
	}
	if problem, archived := archivedProject(project, "add files"); archived {
		p.refuseProjectFiles(w, r, projectID, problem)
		return
	}
	if err == nil && len(uploads) == 0 {
		err = files.FileError("Choose at least one file.")
	}
	if err == nil {
		limit := project.ContextLimit
		_, err = files.IngestUploads(detached(r), p.deps.DB.Write, p.deps.Cfg.FilesDir(), uploads, p.now(), files.Owner{ProjectID: projectID}, &limit)
	}
	var refused files.FileError
	if errors.As(err, &refused) {
		p.refuseProjectFiles(w, r, projectID, err.Error())
		return
	}
	check(err)
	p.projectFiles(w, r, projectID, http.StatusOK, nil)
}

func (p *pages) refuseProjectFiles(w http.ResponseWriter, r *http.Request, projectID int64, problem string) {
	if web.IsPartial(r) {
		p.projectFiles(w, r, projectID, http.StatusUnprocessableEntity, &problem)
		return
	}
	p.fail(w, r, http.StatusUnprocessableEntity, problem)
}

func (p *pages) projectFile(w http.ResponseWriter, r *http.Request) (int64, *files.Row, bool) {
	projectID, errs := pathInt(r, "project_id")
	fileID, more := pathInt(r, "file_id")
	if errs = append(errs, more...); len(errs) > 0 {
		web.Unprocessable(w, errs...)
		return 0, nil, false
	}
	found, err := files.Get(r.Context(), p.deps.DB.Read, fileID)
	check(err)
	if found == nil || found.ProjectID == nil || *found.ProjectID != projectID {
		p.fail(w, r, http.StatusNotFound, "unknown file")
		return 0, nil, false
	}
	return projectID, found, true
}

func (p *pages) projectFileDownload(w http.ResponseWriter, r *http.Request) {
	if _, found, ok := p.projectFile(w, r); ok {
		p.download(w, r, *found)
	}
}

func (p *pages) projectFileDelete(w http.ResponseWriter, r *http.Request) {
	projectID, found, ok := p.projectFile(w, r)
	if !ok {
		return
	}
	project, ok := p.loadProject(w, r, projectID)
	if !ok {
		return
	}
	if problem, archived := archivedProject(project, "remove files"); archived {
		p.refuseProjectFiles(w, r, projectID, problem)
		return
	}
	_, err := files.DeleteOne(detached(r), p.deps.DB.Write, p.remote(), found.ID)
	check(err)
	p.projectFiles(w, r, projectID, http.StatusOK, nil)
}

func filePreview(ctx context.Context, q db.Querier, found files.Row) (map[string]any, error) {
	limit, err := config.Limit(ctx, q, config.KeyPreviewBytes)
	if err != nil {
		return nil, err
	}
	out := tpl.View(found).(map[string]any)
	render, text, rows, truncated, err := previewOf(found, limit)
	if err != nil {
		return nil, err
	}
	out["render"], out["text"], out["rows"], out["truncated"] = render, text, rows, truncated
	return out, nil
}

func previewOf(found files.Row, previewBytes int) (render any, text string, rows [][]string, truncated bool, err error) {
	rows = [][]string{}
	media := found.MediaType
	if files.IsImageType(media) {
		return "image", "", rows, false, nil
	}
	if media == files.PDFType {
		return nil, "", rows, false, nil
	}
	var body *string
	if found.Text == nil {
		info, statErr := os.Stat(found.Path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil, "", rows, false, nil
		}
		raw, readErr := os.ReadFile(found.Path)
		if readErr != nil {
			return nil, "", nil, false, readErr
		}
		truncated = len(raw) > previewBytes
		head := raw[:min(len(raw), sniffBytes)]
		if strings.HasPrefix(media, "text/") || media == "application/json" || media == "application/xml" || bytes.IndexByte(head, 0) < 0 {
			decoded, decodeErr := mailmime.DecodeReplace(raw[:min(len(raw), previewBytes)], "utf-8")
			if decodeErr != nil {
				return nil, "", nil, false, decodeErr
			}
			body = &decoded
		}
	} else if utf8.RuneCountInString(*found.Text) > previewBytes {
		cut := string([]rune(*found.Text)[:previewBytes])
		body, truncated = &cut, true
	} else {
		body = found.Text
	}
	if body == nil {
		return nil, "", rows, false, nil
	}
	suffix := py.Lower(py.PathSuffix(py.PathName(found.Path)))
	switch {
	case slices.Contains(markdownTypes, media) || suffix == ".md" || suffix == ".markdown":
		return "markdown", *body, rows, truncated, nil
	case slices.Contains(tableTypes, media) || suffix == ".csv":
		parsed, csvErr := pyCSV(*body)
		return "table", "", parsed, truncated, csvErr
	}
	return "text", *body, rows, truncated, nil
}
