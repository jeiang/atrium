package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/db"
)

const (
	ProjectCreateName  application.Name = "assistant.project.create"
	ProjectEditName    application.Name = "assistant.project.edit"
	ProjectArchiveName application.Name = "assistant.project.archive"
	ProjectDetailName  application.Name = "assistant.project.detail"
	ProjectListName    application.Name = "assistant.project.list"
	unknownProject                      = "unknown project"
)

type Project struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Instructions string   `json:"instructions"`
	Effort       string   `json:"effort"`
	ContextLimit int64    `json:"context_limit"`
	WikiHubID    *string  `json:"wiki_hub_id"`
	Private      bool     `json:"private"`
	Archived     bool     `json:"archived"`
	CreatedAt    float64  `json:"created_at"`
	UpdatedAt    float64  `json:"updated_at"`
	ThreadCount  int64    `json:"thread_count"`
	LastActivity *float64 `json:"last_activity"`
}

type ProjectFile struct {
	ID        int64   `json:"id"`
	ThreadID  *int64  `json:"thread_id"`
	RunID     *int64  `json:"run_id"`
	Name      string  `json:"name"`
	MediaType string  `json:"media_type"`
	Bytes     int64   `json:"bytes"`
	Path      string  `json:"path"`
	Origin    string  `json:"origin"`
	CreatedAt float64 `json:"created_at"`
	Text      *string `json:"text"`
	Tokens    int64   `json:"tokens"`
	ProjectID *int64  `json:"project_id"`
}

type ProjectFields struct {
	Name         string
	Instructions string
	Effort       string
	ContextLimit string
	Private      bool
}

type ProjectEditInput struct {
	ID int64
	ProjectFields
}

type ProjectArchiveInput struct {
	ID       int64
	Archived bool
}

type ProjectInput struct {
	ID int64
}

type ProjectListInput struct {
	Archived bool
	After    int64
	Limit    int
}

type ProjectChanged struct {
	ID int64
}

type ProjectDetail struct {
	Project Project       `json:"project"`
	Files   []ProjectFile `json:"files"`
}

func describeProjectCommand[I any](name application.Name) application.Definition {
	return application.Describe[I, ProjectChanged](name, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeProjectCreate() application.Definition {
	return describeProjectCommand[ProjectFields](ProjectCreateName)
}

func DescribeProjectEdit() application.Definition {
	return describeProjectCommand[ProjectEditInput](ProjectEditName)
}

func DescribeProjectArchive() application.Definition {
	return describeProjectCommand[ProjectArchiveInput](ProjectArchiveName)
}

func DescribeProjectDetail() application.Definition {
	return application.Describe[ProjectInput, ProjectDetail](ProjectDetailName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeProjectList() application.Definition {
	return application.Describe[ProjectListInput, []Project](ProjectListName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func projectOf(p projects.Project) Project {
	return Project{ID: p.ID, Name: p.Name, Instructions: p.Instructions, Effort: p.Effort, ContextLimit: p.ContextLimit,
		WikiHubID: p.WikiHubID, Private: bool(p.Private), Archived: bool(p.Archived), CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, ThreadCount: p.ThreadCount, LastActivity: p.LastActivity}
}

func fileOf(f files.Row) ProjectFile {
	return ProjectFile{ID: f.ID, ThreadID: f.ThreadID, RunID: f.RunID, Name: f.Name, MediaType: f.MediaType, Bytes: f.Bytes,
		Path: f.Path, Origin: f.Origin, CreatedAt: f.CreatedAt, Text: f.Text, Tokens: f.Tokens, ProjectID: f.ProjectID}
}

func (f ProjectFields) fields() projects.Fields {
	return projects.Fields{Name: f.Name, Instructions: f.Instructions, Effort: f.Effort, ContextLimit: f.ContextLimit, Private: f.Private}
}

func projectInvalid(err error) error {
	var refused projects.ProjectError
	if errors.As(err, &refused) {
		return &application.Error{Code: application.Invalid, Message: refused.Error(), Cause: err}
	}
	return err
}

var projectScopes = []string{state.Assistant}

func (s *Service) ProjectList(ctx context.Context, in ProjectListInput) (application.Read[[]Project], error) {
	out := []Project{}
	rev, err := s.snapshot(ctx, string(ProjectListName), projectScopes, in, func(q db.Querier) error {
		var rows []projects.Project
		var err error
		if in.Limit > 0 {
			rows, err = projects.Page(ctx, q, in.Archived, in.After, in.Limit)
		} else {
			rows, err = projects.List(ctx, q, in.Archived)
		}
		for _, row := range rows {
			out = append(out, projectOf(row))
		}
		return err
	})
	return application.Read[[]Project]{Revision: rev, Data: out}, err
}

func (s *Service) ProjectDetail(ctx context.Context, in ProjectInput) (application.Read[ProjectDetail], error) {
	var out ProjectDetail
	rev, err := s.snapshot(ctx, string(ProjectDetailName), projectScopes, in, func(q db.Querier) error {
		found, err := projects.Get(ctx, q, in.ID)
		if err != nil {
			return err
		}
		if found == nil {
			return notFound(unknownProject)
		}
		rows, err := files.OfProject(ctx, q, in.ID)
		out = ProjectDetail{Project: projectOf(*found), Files: make([]ProjectFile, 0, len(rows))}
		for _, row := range rows {
			out.Files = append(out.Files, fileOf(row))
		}
		return err
	})
	return application.Read[ProjectDetail]{Revision: rev, Data: out}, err
}

func (s *Service) projectCommand(ctx context.Context, name application.Name, id int64, apply func(tx *sql.Tx) (int64, error)) (application.Change[ProjectChanged], error) {
	var auditID int64
	err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		changed, err := apply(tx)
		if err != nil {
			return err
		}
		id = changed
		auditID, err = s.audit(ctx, tx, name, target("project", id), state.OK)
		return err
	})
	if err != nil {
		return application.Change[ProjectChanged]{}, s.reject(ctx, name, target("project", id), projectInvalid(err))
	}
	return application.Change[ProjectChanged]{Data: ProjectChanged{ID: id}, AuditID: auditID}, nil
}

func (s *Service) ProjectCreate(ctx context.Context, in ProjectFields) (application.Change[ProjectChanged], error) {
	now := s.now()
	return s.projectCommand(ctx, ProjectCreateName, 0, func(tx *sql.Tx) (int64, error) {
		return projects.Create(ctx, tx, now, in.fields())
	})
}

func (s *Service) ProjectEdit(ctx context.Context, in ProjectEditInput) (application.Change[ProjectChanged], error) {
	now := s.now()
	return s.projectCommand(ctx, ProjectEditName, in.ID, func(tx *sql.Tx) (int64, error) {
		found, err := projects.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, err
		}
		if found == nil {
			return 0, notFound(unknownProject)
		}
		_, err = projects.Update(ctx, tx, in.ID, now, in.fields())
		return in.ID, err
	})
}

func (s *Service) ProjectArchive(ctx context.Context, in ProjectArchiveInput) (application.Change[ProjectChanged], error) {
	return s.projectCommand(ctx, ProjectArchiveName, in.ID, func(tx *sql.Tx) (int64, error) {
		found, err := projects.SetArchived(ctx, tx, in.ID, in.Archived)
		if err == nil && !found {
			err = notFound(unknownProject)
		}
		return in.ID, err
	})
}
