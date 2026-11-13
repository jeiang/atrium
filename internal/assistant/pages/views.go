package pages

import (
	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/journal"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/assistant/service"
)

type ThreadPage struct {
	View     *asstapp.Transcript  `json:"view"`
	ThreadID int64                `json:"thread_id"`
	History  bool                 `json:"history"`
	Composer asstapp.ComposerView `json:"composer"`
	Work     asstapp.Work         `json:"work"`
	Projects []asstapp.Project    `json:"projects"`
	Project  *asstapp.Project     `json:"project"`
}

type QueuedData struct {
	Queued []asstapp.Queued `json:"queued"`
}

type FilePreview struct {
	ID        int64      `json:"id"`
	ThreadID  *int64     `json:"thread_id"`
	RunID     *int64     `json:"run_id"`
	Name      string     `json:"name"`
	MediaType string     `json:"media_type"`
	Bytes     int64      `json:"bytes"`
	Path      string     `json:"path"`
	Origin    string     `json:"origin"`
	CreatedAt float64    `json:"created_at"`
	Text      *string    `json:"text"`
	Tokens    int64      `json:"tokens"`
	ProjectID *int64     `json:"project_id"`
	Render    string     `json:"render"`
	Body      string     `json:"body"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated"`
}

type FileData struct {
	File    FilePreview `json:"file"`
	FileURL string      `json:"file_url"`
}

type ProjectFiles struct {
	Project *projects.Project `json:"project"`
	Files   []files.Row       `json:"files"`
	Error   *string           `json:"error"`
}

type OptionsSaved struct {
	View     *asstapp.Transcript  `json:"view"`
	Composer asstapp.ComposerView `json:"composer"`
}

type RunStarted struct {
	View     *asstapp.Transcript `json:"view"`
	ThreadID int64               `json:"thread_id"`
	RunID    int64               `json:"run_id"`
	Items    []*service.Item     `json:"items"`
	After    int64               `json:"after"`
}

type RunLive struct {
	View     *asstapp.Transcript `json:"view"`
	ThreadID int64               `json:"thread_id"`
	RunID    int64               `json:"run_id"`
	After    int64               `json:"after"`
}

type RunDone struct {
	View     *asstapp.Transcript `json:"view"`
	ThreadID int64               `json:"thread_id"`
	Run      *journal.Run        `json:"run"`
	Items    []*service.Item     `json:"items"`
}
