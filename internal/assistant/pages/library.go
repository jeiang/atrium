package pages

import (
	"fmt"
	"net/http"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
)

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

func libraryFile(e asstapp.LibraryEntry) LibraryFile {
	f := LibraryFile{ID: e.ID, Name: e.Name, MediaType: e.MediaType, Bytes: e.Bytes, CreatedAt: e.CreatedAt}
	if e.ThreadID != 0 {
		f.Owner = "Untitled thread"
		if e.ThreadTitle != "" {
			f.Owner = e.ThreadTitle
		}
		f.OwnerURL = fmt.Sprintf("/chat/%d", e.ThreadID)
		f.DownloadURL = fmt.Sprintf("/chat/%d/files/%d/download", e.ThreadID, f.ID)
		return f
	}
	f.Owner, f.Project = e.ProjectName, true
	f.OwnerURL = fmt.Sprintf("/projects/%d/edit", e.ProjectID)
	f.DownloadURL = fmt.Sprintf("/projects/%d/files/%d/download", e.ProjectID, f.ID)
	return f
}

func (p *pages) libraryFiles(w http.ResponseWriter, r *http.Request) {
	read, err := p.assistant().FileList(r.Context(), asstapp.FileListInput{})
	check(err)
	found := make([]LibraryFile, len(read.Data.Library))
	for i, e := range read.Data.Library {
		found[i] = libraryFile(e)
	}
	p.render(w, r, http.StatusOK, "library_files", "library_files.gohtml", LibraryFilesData{Files: found}, nil)
}
