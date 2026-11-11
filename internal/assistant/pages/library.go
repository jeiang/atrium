package pages

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
)

const libraryFilesQuery = "SELECT f.id, f.name, f.media_type, f.bytes, f.created_at, f.thread_id, t.title, f.project_id, p.name " +
	"FROM thread_files f LEFT JOIN threads t ON t.id = f.thread_id LEFT JOIN projects p ON p.id = f.project_id " +
	"WHERE f.thread_id IS NOT NULL OR f.project_id IS NOT NULL ORDER BY f.created_at DESC, f.id DESC LIMIT ?"

type LibraryFile struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	MediaType   string  `json:"media_type"`
	Bytes       int64   `json:"bytes"`
	CreatedAt   float64 `json:"created_at"`
	Owner       string  `json:"owner"`
	OwnerURL    string  `json:"owner_url"`
	Project     bool    `json:"project"`
	DownloadURL string  `json:"download_url"`
}

type LibraryFilesData struct {
	Files []LibraryFile `json:"files"`
}

func scanLibraryFile(r *sql.Rows) (LibraryFile, error) {
	var f LibraryFile
	var threadID, projectID sql.NullInt64
	var title, project sql.NullString
	if err := r.Scan(&f.ID, &f.Name, &f.MediaType, &f.Bytes, &f.CreatedAt, &threadID, &title, &projectID, &project); err != nil {
		return f, err
	}
	if threadID.Valid {
		f.Owner = "Untitled thread"
		if title.String != "" {
			f.Owner = title.String
		}
		f.OwnerURL = fmt.Sprintf("/chat/%d", threadID.Int64)
		f.DownloadURL = fmt.Sprintf("/chat/%d/files/%d/download", threadID.Int64, f.ID)
		return f, nil
	}
	f.Owner, f.Project = project.String, true
	f.OwnerURL = fmt.Sprintf("/projects/%d/edit", projectID.Int64)
	f.DownloadURL = fmt.Sprintf("/projects/%d/files/%d/download", projectID.Int64, f.ID)
	return f, nil
}

func (p *pages) libraryFiles(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit := config.TuningWebPageSize
	rows, err := p.deps.DB.Read.QueryContext(ctx, libraryFilesQuery, limit)
	check(err)
	found, err := db.Collect(rows, scanLibraryFile)
	check(err)
	p.render(w, r, http.StatusOK, "library_files", "library_files.gohtml", LibraryFilesData{Files: found}, nil)
}
