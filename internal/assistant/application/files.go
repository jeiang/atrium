package application

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
)

var log = logx.Logger("atrium.agent.application")

const (
	ProjectFileUploadName application.Name = "assistant.project-file.upload"
	ProjectFileDeleteName application.Name = "assistant.project-file.delete"
	FileWriteName         application.Name = "assistant.file.write"
	FilePreviewName       application.Name = "assistant.file.preview"
	FileDownloadName      application.Name = "assistant.file.download"
	FileListName          application.Name = "assistant.file.list"
	FileReadName          application.Name = "assistant.file.read"
	FileSearchName        application.Name = "assistant.file.search"

	unknownFile    = "unknown file"
	missingStorage = "file is missing from storage"
	sniffBytes     = 4096
	searchHits     = 50
)

const libraryFilesQuery = "SELECT f.id, f.name, f.media_type, f.bytes, f.created_at, f.thread_id, t.title, f.project_id, p.name " +
	"FROM thread_files f LEFT JOIN threads t ON t.id = f.thread_id LEFT JOIN projects p ON p.id = f.project_id " +
	"WHERE f.thread_id IS NOT NULL OR f.project_id IS NOT NULL ORDER BY f.created_at DESC, f.id DESC LIMIT ?"

var (
	nameRe        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	markdownTypes = []string{"text/markdown", "text/x-markdown"}
	tableTypes    = []string{"text/csv", "application/csv", files.XLSXType}
)

type FileInput struct {
	ThreadID  int64
	ProjectID int64
	FileID    int64
}

type DownloadInput struct {
	FileInput
	Raw bool
}

type FileListInput struct {
	ThreadID  int64
	ProjectID int64
}

type ProjectUploadInput struct {
	ProjectID int64
	Uploads   []files.UploadedFile
}

type FileReadInput struct {
	ThreadID int64
	Name     string
}

type FileSearchInput struct {
	ThreadID int64
	Query    string
}

type FileWriteInput struct {
	ThreadID   int64
	RunID      int64
	ProposalID *int64
	Name       string
	Content    string
}

type FileView struct {
	File      files.Row  `json:"file"`
	Render    string     `json:"render"`
	Body      string     `json:"body"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

type FileStream struct {
	Name      string
	MediaType string
	ModTime   time.Time
	File      *os.File
}

type LibraryEntry struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	MediaType   string  `json:"media_type"`
	Bytes       int64   `json:"bytes"`
	CreatedAt   float64 `json:"created_at"`
	ThreadID    int64   `json:"thread_id,omitempty"`
	ThreadTitle string  `json:"thread_title,omitempty"`
	ProjectID   int64   `json:"project_id,omitempty"`
	ProjectName string  `json:"project_name,omitempty"`
}

type FileList struct {
	Project *projects.Project `json:"project,omitempty"`
	Files   []files.Row       `json:"files"`
	Library []LibraryEntry    `json:"library"`
}

type FileText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type FileChanged struct {
	ProjectID int64   `json:"project_id,omitempty"`
	FileIDs   []int64 `json:"file_ids,omitempty"`
	EffectID  int64   `json:"effect_id,omitempty"`
}

func DescribeProjectFileUpload() application.Definition {
	return application.Describe[ProjectUploadInput, FileChanged](ProjectFileUploadName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeProjectFileDelete() application.Definition {
	return application.Describe[FileInput, FileChanged](ProjectFileDeleteName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeFileWrite() application.Definition {
	return application.Describe[FileWriteInput, FileChanged](FileWriteName, application.Command, []application.Authority{application.Agent}, application.External, true)
}

func DescribeFilePreview() application.Definition {
	return describeRead[FileInput, FileView](FilePreviewName)
}

func DescribeFileDownload() application.Definition {
	return describeRead[DownloadInput, FileStream](FileDownloadName)
}

func DescribeFileList() application.Definition {
	return describeRead[FileListInput, FileList](FileListName)
}

func DescribeFileRead() application.Definition {
	return application.Describe[FileReadInput, FileText](FileReadName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}

func DescribeFileSearch() application.Definition {
	return application.Describe[FileSearchInput, []string](FileSearchName, application.Query, []application.Authority{application.Agent}, application.NoEffect, false)
}

func (s *Service) WithFiles(dir string) *Service {
	out := *s
	out.filesDir = dir
	return &out
}

func (s *Service) WithLedger(l *ledger.Store) *Service {
	out := *s
	out.ledger = l
	return &out
}

func (s *Service) locate(ctx context.Context, q db.Querier, in FileInput) (*files.Row, error) {
	owner := files.ProjectOwner(in.ProjectID)
	if in.ThreadID != 0 {
		thread, err := threads.Get(ctx, q, in.ThreadID)
		if err != nil {
			return nil, err
		}
		if thread == nil {
			return nil, notFound("unknown thread")
		}
		owner = files.ThreadOwner(in.ThreadID)
	}
	found, err := files.Get(ctx, q, in.FileID)
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, notFound(unknownFile)
	}
	if got, owned := files.OwnerOf(found); !owned || got != owner {
		return nil, notFound(unknownFile)
	}
	return found, nil
}

func (s *Service) FilePreview(ctx context.Context, in FileInput) (application.Read[FileView], error) {
	var view FileView
	rev, err := s.snapshot(ctx, string(FilePreviewName), []string{"assistant"}, in, func(q db.Querier) error {
		found, err := s.locate(ctx, q, in)
		if err != nil {
			return err
		}
		view, err = previewOf(*found, config.TuningPreviewBytes)
		return err
	})
	if err != nil {
		return application.Read[FileView]{}, err
	}
	return application.Read[FileView]{Revision: rev, Data: view}, nil
}

func (s *Service) FileDownload(ctx context.Context, in DownloadInput) (application.Read[FileStream], error) {
	var found *files.Row
	rev, err := s.snapshot(ctx, string(FileDownloadName), []string{"assistant"}, in, func(q db.Querier) error {
		var err error
		found, err = s.locate(ctx, q, in.FileInput)
		return err
	})
	if err != nil {
		return application.Read[FileStream]{}, err
	}
	if in.Raw && !files.IsImageType(found.MediaType) {
		return application.Read[FileStream]{}, notFound("not an image")
	}
	f, err := os.Open(found.Path)
	if err != nil {
		return application.Read[FileStream]{}, notFound(missingStorage)
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return application.Read[FileStream]{}, notFound(missingStorage)
	}
	return application.Read[FileStream]{Revision: rev, Data: FileStream{Name: found.Name, MediaType: found.MediaType, ModTime: info.ModTime(), File: f}}, nil
}

func (s *Service) FileList(ctx context.Context, in FileListInput) (application.Read[FileList], error) {
	out := FileList{Files: []files.Row{}, Library: []LibraryEntry{}}
	rev, err := s.snapshot(ctx, string(FileListName), []string{"assistant"}, in, func(q db.Querier) error {
		if in.ThreadID != 0 {
			thread, err := threads.Get(ctx, q, in.ThreadID)
			if err != nil {
				return err
			}
			if thread == nil {
				return notFound("unknown thread")
			}
			rows, err := files.OfThread(ctx, q, in.ThreadID)
			if rows != nil {
				out.Files = rows
			}
			return err
		}
		if in.ProjectID == 0 {
			rows, err := q.QueryContext(ctx, libraryFilesQuery, config.TuningWebPageSize)
			if err != nil {
				return err
			}
			found, err := db.Collect(rows, scanLibraryEntry)
			if found != nil {
				out.Library = found
			}
			return err
		}
		project, err := projects.Get(ctx, q, in.ProjectID)
		if err != nil {
			return err
		}
		if project == nil {
			return notFound(unknownProject)
		}
		out.Project = project
		rows, err := files.OfProject(ctx, q, in.ProjectID)
		if rows != nil {
			out.Files = rows
		}
		return err
	})
	if err != nil {
		return application.Read[FileList]{}, err
	}
	return application.Read[FileList]{Revision: rev, Data: out}, nil
}

func scanLibraryEntry(r *sql.Rows) (LibraryEntry, error) {
	var e LibraryEntry
	var threadID, projectID sql.NullInt64
	var title, project sql.NullString
	err := r.Scan(&e.ID, &e.Name, &e.MediaType, &e.Bytes, &e.CreatedAt, &threadID, &title, &projectID, &project)
	e.ThreadID, e.ThreadTitle, e.ProjectID, e.ProjectName = threadID.Int64, title.String, projectID.Int64, project.String
	return e, err
}

func (s *Service) writableProject(ctx context.Context, projectID int64, verb string) (*projects.Project, error) {
	project, err := projects.Get(ctx, s.db.Read, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, notFound(unknownProject)
	}
	return project, archivedProject(project, verb)
}

func (s *Service) ProjectFileUpload(ctx context.Context, in ProjectUploadInput) (application.Change[FileChanged], error) {
	tgt := target("project", in.ProjectID)
	changed, err := func() (FileChanged, error) {
		project, err := s.writableProject(ctx, in.ProjectID, "add files")
		if err != nil {
			return FileChanged{}, err
		}
		if len(in.Uploads) == 0 {
			return FileChanged{}, files.FileError("Choose at least one file.")
		}
		limit := project.ContextLimit
		blocks, err := files.IngestUploads(ctx, s.db, s.filesDir, in.Uploads, s.now(), files.ProjectOwner(in.ProjectID), &limit)
		out := FileChanged{ProjectID: in.ProjectID}
		for _, b := range blocks {
			out.FileIDs = append(out.FileIDs, b.FileID)
		}
		return out, err
	}()
	if err != nil {
		return application.Change[FileChanged]{}, s.reject(ctx, ProjectFileUploadName, tgt, classify(err))
	}
	auditID, err := s.audit(ctx, nil, ProjectFileUploadName, tgt, state.OK)
	return application.Change[FileChanged]{Data: changed, AuditID: auditID}, err
}

func (s *Service) ProjectFileDelete(ctx context.Context, in FileInput) (application.Change[FileChanged], error) {
	tgt := target("project", in.ProjectID)
	var auditID int64
	var path string
	err := func() error {
		found, err := s.locate(ctx, s.db.Read, FileInput{ProjectID: in.ProjectID, FileID: in.FileID})
		if err != nil {
			return err
		}
		if _, err := s.writableProject(ctx, in.ProjectID, "remove files"); err != nil {
			return err
		}
		return s.db.WithTx(ctx, func(tx *sql.Tx) error {
			var err error
			if path, _, err = files.DeleteRow(ctx, tx, found.ID); err != nil {
				return err
			}
			auditID, err = s.audit(ctx, tx, ProjectFileDeleteName, tgt, state.OK)
			return err
		})
	}()
	if err != nil {
		return application.Change[FileChanged]{}, s.reject(ctx, ProjectFileDeleteName, tgt, classify(err))
	}
	if path != "" {
		if err := files.RemoveFile(path); err != nil {
			log.Warn("could not remove a deleted project file from disk", logx.Err, err.Error())
		}
	}
	return application.Change[FileChanged]{Data: FileChanged{ProjectID: in.ProjectID, FileIDs: []int64{in.FileID}}, AuditID: auditID}, nil
}

func previewOf(found files.Row, previewBytes int) (FileView, error) {
	view := FileView{File: found, Rows: [][]string{}}
	media := found.MediaType
	if files.IsImageType(media) {
		view.Render = "image"
		return view, nil
	}
	if media == files.PDFType {
		return view, nil
	}
	var body *string
	if found.Text == nil {
		info, statErr := os.Stat(found.Path)
		if statErr != nil || !info.Mode().IsRegular() {
			return view, nil
		}
		raw, err := os.ReadFile(found.Path)
		if err != nil {
			return FileView{}, err
		}
		view.Truncated = len(raw) > previewBytes
		head := raw[:min(len(raw), sniffBytes)]
		if strings.HasPrefix(media, "text/") || media == "application/json" || media == "application/xml" || bytes.IndexByte(head, 0) < 0 {
			decoded := httpx.DecodeReplace(raw[:min(len(raw), previewBytes)])
			body = &decoded
		}
	} else if utf8.RuneCountInString(*found.Text) > previewBytes {
		cut := string([]rune(*found.Text)[:previewBytes])
		body, view.Truncated = &cut, true
	} else {
		body = found.Text
	}
	if body == nil {
		view.Truncated = false
		return view, nil
	}
	suffix := strings.ToLower(path.Ext(path.Base(found.Path)))
	switch {
	case slices.Contains(markdownTypes, media) || suffix == ".md" || suffix == ".markdown":
		view.Render, view.Body = "markdown", *body
		return view, nil
	case slices.Contains(tableTypes, media) || suffix == ".csv":
		parsed, err := pyCSV(*body)
		view.Render, view.Rows = "table", parsed
		return view, err
	}
	view.Render, view.Body = "text", *body
	return view, nil
}

func refusedFile(message string) error {
	return &application.Error{Code: application.Refused, Message: message}
}

func (s *Service) FileRead(ctx context.Context, in FileReadInput) (application.Read[FileText], error) {
	name, err := fileName(in.Name)
	if err != nil {
		return application.Read[FileText]{}, err
	}
	dirs, err := s.fileRoots(ctx, in.ThreadID)
	if err != nil {
		return application.Read[FileText]{}, err
	}
	data, err := os.ReadFile(resolveFile(dirs, name))
	if errors.Is(err, os.ErrNotExist) {
		return application.Read[FileText]{}, notFound("no such file: " + name)
	}
	if err != nil {
		return application.Read[FileText]{}, err
	}
	limits, err := files.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return application.Read[FileText]{}, err
	}
	if len(data) > limits.FileCap {
		return application.Read[FileText]{}, refusedFile(fmt.Sprintf("%s is larger than the %d byte cap", name, limits.FileCap))
	}
	if !utf8.Valid(data) {
		return application.Read[FileText]{}, refusedFile(name + " is not a text file")
	}
	return application.Read[FileText]{Data: FileText{Name: name, Text: string(data)}}, nil
}

func (s *Service) FileSearch(ctx context.Context, in FileSearchInput) (application.Read[[]string], error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return application.Read[[]string]{}, invalid("query", "query is required", nil)
	}
	dirs, err := s.fileRoots(ctx, in.ThreadID)
	if err != nil {
		return application.Read[[]string]{}, err
	}
	limits, err := files.ReadLimits(ctx, s.db.Read)
	if err != nil {
		return application.Read[[]string]{}, err
	}
	out := []string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return application.Read[[]string]{}, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !nameRe.MatchString(entry.Name()) {
				continue
			}
			if strings.Contains(entry.Name(), query) {
				out = append(out, entry.Name())
			}
			if len(out) >= searchHits {
				return application.Read[[]string]{Data: out}, nil
			}
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil || len(data) > limits.FileCap || !utf8.Valid(data) {
				continue
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if strings.Contains(line, query) {
					out = append(out, entry.Name()+": "+clip(strings.TrimSpace(line), 200))
					if len(out) >= searchHits {
						return application.Read[[]string]{Data: out}, nil
					}
				}
			}
		}
	}
	return application.Read[[]string]{Data: out}, nil
}

func (s *Service) FileWrite(ctx context.Context, in FileWriteInput) (application.Change[FileChanged], error) {
	tgt := target("thread", in.ThreadID)
	changed, err := s.writeFile(ctx, in)
	if err != nil {
		return application.Change[FileChanged]{}, s.reject(ctx, FileWriteName, tgt, err)
	}
	auditID, err := s.state.Audit(ctx, nil, state.Entry{Operation: string(FileWriteName), Outcome: state.OK, Target: tgt,
		DetailsJSON: fmt.Sprintf(`{"effect_ids":[%d]}`, changed.EffectID)})
	return application.Change[FileChanged]{Data: changed, AuditID: auditID, EffectIDs: []int64{changed.EffectID}}, err
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
