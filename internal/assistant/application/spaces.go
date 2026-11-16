package application

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/tasks"
)

const (
	SpaceCreateName  application.Name = "knowledge.space.create"
	SpaceEditName    application.Name = "knowledge.space.edit"
	SpaceArchiveName application.Name = "knowledge.space.archive"
	SpaceDetailName  application.Name = "knowledge.space.detail"
	SpaceListName    application.Name = "knowledge.space.list"
	unknownSpace                      = "unknown space"
	spaceTaskLink                     = "project"
)

type Space struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Instructions string   `json:"instructions"`
	Effort       string   `json:"effort"`
	ContextLimit int64    `json:"context_limit"`
	Private      bool     `json:"private"`
	Archived     bool     `json:"archived"`
	CreatedAt    float64  `json:"created_at"`
	UpdatedAt    float64  `json:"updated_at"`
	ThreadCount  int64    `json:"thread_count"`
	LastActivity *float64 `json:"last_activity"`
}

type SpaceThread struct {
	ID        int64   `json:"id"`
	Title     *string `json:"title"`
	Archived  bool    `json:"archived"`
	Pinned    bool    `json:"pinned"`
	UpdatedAt float64 `json:"updated_at"`
}

type SpaceTask struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Status   string   `json:"status" api:"enum=open|done|dismissed"`
	Due      *string  `json:"due"`
	RemindAt *float64 `json:"remind_at"`
}

type SpaceRecord struct {
	LinkID    int64   `json:"link_id"`
	Relation  string  `json:"relation"`
	ID        int64   `json:"id"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	State     string  `json:"state" api:"enum=live|retracted"`
	Address   string  `json:"address"`
	CreatedAt float64 `json:"created_at"`
}

type SpaceCounts struct {
	Files   int `json:"files"`
	Threads int `json:"threads"`
	Tasks   int `json:"tasks"`
	Records int `json:"records"`
}

type SpaceFile struct {
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
	SpaceID   *int64  `json:"space_id"`
}

type SpaceFields struct {
	Name         string
	Instructions string
	Effort       string
	ContextLimit string
	Private      bool
}

type SpaceEditInput struct {
	ID int64
	SpaceFields
}

type SpaceArchiveInput struct {
	ID       int64
	Archived bool
}

type SpaceInput struct {
	ID int64
}

type SpaceListInput struct {
	Archived bool
	After    int64
	Limit    int
}

type SpaceChanged struct {
	ID int64
}

type SpaceDetail struct {
	Space    Space            `json:"space"`
	Sections []kb.PageSection `json:"-"`
	Files    []SpaceFile      `json:"files"`
	Threads  []SpaceThread    `json:"threads"`
	Tasks    []SpaceTask      `json:"tasks"`
	Records  []SpaceRecord    `json:"records"`
	Counts   SpaceCounts      `json:"counts"`
}

func describeSpaceCommand[I any](name application.Name) application.Definition {
	return application.Describe[I, SpaceChanged](name, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}

func DescribeSpaceCreate() application.Definition {
	return describeSpaceCommand[SpaceFields](SpaceCreateName)
}

func DescribeSpaceEdit() application.Definition {
	return describeSpaceCommand[SpaceEditInput](SpaceEditName)
}

func DescribeSpaceArchive() application.Definition {
	return describeSpaceCommand[SpaceArchiveInput](SpaceArchiveName)
}

func DescribeSpaceDetail() application.Definition {
	return application.Describe[SpaceInput, SpaceDetail](SpaceDetailName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func DescribeSpaceList() application.Definition {
	return application.Describe[SpaceListInput, []Space](SpaceListName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func spaceOf(p projects.Project) Space {
	return Space{ID: p.ID, Name: p.Name, Instructions: p.Instructions, Effort: p.Effort, ContextLimit: p.ContextLimit,
		Private: bool(p.Private), Archived: bool(p.Archived), CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt, ThreadCount: p.ThreadCount, LastActivity: p.LastActivity}
}

func fileOf(f files.Row) SpaceFile {
	return SpaceFile{ID: f.ID, ThreadID: f.ThreadID, RunID: f.RunID, Name: f.Name, MediaType: f.MediaType, Bytes: f.Bytes,
		Path: f.Path, Origin: f.Origin, CreatedAt: f.CreatedAt, Text: f.Text, Tokens: f.Tokens, SpaceID: f.ProjectID}
}

func (f SpaceFields) fields() projects.Fields {
	return projects.Fields{Name: f.Name, Instructions: f.Instructions, Effort: f.Effort, ContextLimit: f.ContextLimit, Private: f.Private}
}

func projectInvalid(err error) error {
	var refused projects.ProjectError
	if errors.As(err, &refused) {
		return &application.Error{Code: application.Invalid, Message: refused.Error(), Cause: err}
	}
	return err
}

var (
	projectScopes = []string{state.Assistant}
	spaceScopes   = []string{state.Assistant, state.Knowledge, state.Tasks}
)

func (s *Service) SpaceList(ctx context.Context, in SpaceListInput) (application.Read[[]Space], error) {
	out := []Space{}
	rev, err := s.snapshot(ctx, string(SpaceListName), projectScopes, in, func(q db.Querier) error {
		var rows []projects.Project
		var err error
		if in.Limit > 0 {
			rows, err = projects.Page(ctx, q, in.Archived, in.After, in.Limit)
		} else {
			rows, err = projects.List(ctx, q, in.Archived)
		}
		for _, row := range rows {
			out = append(out, spaceOf(row))
		}
		return err
	})
	return application.Read[[]Space]{Revision: rev, Data: out}, err
}

func (s *Service) SpaceDetail(ctx context.Context, in SpaceInput) (application.Read[SpaceDetail], error) {
	var out SpaceDetail
	rev, err := s.snapshot(ctx, string(SpaceDetailName), spaceScopes, in, func(q db.Querier) error {
		found, err := projects.Get(ctx, q, in.ID)
		if err != nil {
			return err
		}
		if found == nil {
			return notFound(unknownSpace)
		}
		out, err = spaceDetail(ctx, q, *found)
		return err
	})
	return application.Read[SpaceDetail]{Revision: rev, Data: out}, err
}

func spaceDetail(ctx context.Context, q db.Querier, found projects.Project) (SpaceDetail, error) {
	id := strconv.FormatInt(found.ID, 10)
	out := SpaceDetail{Space: spaceOf(found), Sections: kb.Sections(found.Instructions), Files: []SpaceFile{}, Threads: []SpaceThread{},
		Tasks: []SpaceTask{}, Records: []SpaceRecord{}}
	rows, err := files.OfProject(ctx, q, found.ID)
	if err != nil {
		return out, err
	}
	for _, row := range rows {
		out.Files = append(out.Files, fileOf(row))
	}
	threads, err := projects.Threads(ctx, q, found.ID)
	if err != nil {
		return out, err
	}
	for _, t := range threads {
		out.Threads = append(out.Threads, SpaceThread{ID: t.ID, Title: t.Title, Archived: bool(t.Archived), Pinned: bool(t.Pinned), UpdatedAt: t.UpdatedAt})
	}
	linked, err := tasks.OfSource(ctx, q, spaceTaskLink, id)
	if err != nil {
		return out, err
	}
	for _, t := range linked {
		out.Tasks = append(out.Tasks, SpaceTask{ID: t.ID, Title: t.Title, Status: t.Status, Due: t.Due, RemindAt: t.RemindAt})
	}
	links, err := kb.LinksTo(ctx, q, kb.TargetSpace, id)
	if err != nil {
		return out, err
	}
	for _, l := range links {
		rec, err := kb.Get(ctx, q, l.RecordID)
		if err != nil {
			return out, err
		}
		if rec == nil {
			continue
		}
		state := "live"
		if rec.Retracted() {
			state = "retracted"
		}
		out.Records = append(out.Records, SpaceRecord{LinkID: l.ID, Relation: l.Relation, ID: rec.ID, Title: rec.Title, Kind: kb.KindOf(*rec),
			State: state, Address: "/knowledge/" + strconv.FormatInt(rec.ID, 10), CreatedAt: l.CreatedAt})
	}
	out.Counts = SpaceCounts{Files: len(out.Files), Threads: len(out.Threads), Tasks: len(out.Tasks), Records: len(out.Records)}
	return out, nil
}

func (s *Service) spaceCommand(ctx context.Context, name application.Name, id int64, apply func(tx *sql.Tx) (int64, error)) (application.Change[SpaceChanged], error) {
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
		return application.Change[SpaceChanged]{}, s.reject(ctx, name, target("project", id), projectInvalid(err))
	}
	return application.Change[SpaceChanged]{Data: SpaceChanged{ID: id}, AuditID: auditID}, nil
}

func (s *Service) SpaceCreate(ctx context.Context, in SpaceFields) (application.Change[SpaceChanged], error) {
	now := s.now()
	return s.spaceCommand(ctx, SpaceCreateName, 0, func(tx *sql.Tx) (int64, error) {
		return projects.Create(ctx, tx, now, in.fields())
	})
}

func (s *Service) SpaceEdit(ctx context.Context, in SpaceEditInput) (application.Change[SpaceChanged], error) {
	now := s.now()
	return s.spaceCommand(ctx, SpaceEditName, in.ID, func(tx *sql.Tx) (int64, error) {
		found, err := projects.Get(ctx, tx, in.ID)
		if err != nil {
			return 0, err
		}
		if found == nil {
			return 0, notFound(unknownSpace)
		}
		_, err = projects.Update(ctx, tx, in.ID, now, in.fields())
		return in.ID, err
	})
}

func (s *Service) SpaceArchive(ctx context.Context, in SpaceArchiveInput) (application.Change[SpaceChanged], error) {
	return s.spaceCommand(ctx, SpaceArchiveName, in.ID, func(tx *sql.Tx) (int64, error) {
		found, err := projects.SetArchived(ctx, tx, in.ID, in.Archived)
		if err == nil && !found {
			err = notFound(unknownSpace)
		}
		return in.ID, err
	})
}
