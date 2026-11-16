package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/assistant/block"
	"github.com/jeiang/atrium/internal/assistant/files"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/day"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/module"
)

const (
	HomeName        application.Name = "assistant.home"
	AboutName       application.Name = "assistant.about"
	TodayEnsureName application.Name = "assistant.today.ensure"
	AllThreads                       = "all"
	ArchivedThreads                  = "archived"
)

var homeScopes = []string{state.Assistant, state.Settings, state.Calendar, state.Tasks, state.Attention, state.Mail, state.Knowledge}

type AgendaEvent struct {
	Time  string `json:"time"`
	Title string `json:"title"`
	Meta  string `json:"meta"`
	State string `json:"state"`
	Ask   string `json:"ask"`
}

type Agenda struct {
	Events []AgendaEvent `json:"events"`
	Next   *AgendaEvent  `json:"next"`
}

type About struct {
	Ref     string `json:"ref"`
	Label   string `json:"label"`
	Line    string `json:"-"`
	Project int64  `json:"-"`
}

type HomeInput struct {
	Q      string
	View   string
	About  string
	Limit  int
	List   bool
	Agenda bool
}

type Home struct {
	Threads  []threads.Listed   `json:"threads"`
	Q        string             `json:"q"`
	View     string             `json:"view"`
	Welcome  bool               `json:"welcome"`
	Date     string             `json:"date"`
	Digest   []block.DigestLine `json:"digest"`
	Agenda   bool               `json:"agenda"`
	About    *About             `json:"about"`
	Defaults threads.Options    `json:"defaults"`
	Composer ComposerView       `json:"composer"`
	Day      *Agenda            `json:"-"`
}

type AboutInput struct {
	Ref string
}

type TodayInput struct{}

type TodayResult struct {
	ThreadID int64
}

func DescribeHome() application.Definition { return describeRead[HomeInput, Home](HomeName) }

func DescribeAbout() application.Definition { return describeRead[AboutInput, *About](AboutName) }

func DescribeTodayEnsure() application.Definition {
	return application.Describe[TodayInput, TodayResult](TodayEnsureName, application.Command,
		[]application.Authority{application.Operator}, application.NoEffect, true)
}

var aboutFile = regexp.MustCompile(`^[A-Za-z0-9._ ()+-]{1,200}$`)

func Refs() []module.RefKind {
	return []module.RefKind{
		module.NumberedRef("knowledge", func(ctx context.Context, q db.Querier, id int64, number string) (*module.Ref, error) {
			label, err := kb.Title(ctx, q, id)
			return &module.Ref{Label: label, Line: "About knowledge record " + number + "."}, err
		}),
		module.NumberedRef("file", func(ctx context.Context, q db.Querier, id int64, _ string) (*module.Ref, error) {
			label, project, err := files.ProjectFileName(ctx, q, id)
			if err != nil || !aboutFile.MatchString(label) {
				return nil, err
			}
			return &module.Ref{Label: label, Project: project, Line: "About the project file " + strconv.Quote(label) + "."}, nil
		}),
	}
}

func (s *Service) day(ctx context.Context, now float64) (time.Time, error) {
	loc, err := config.From(ctx).Location(s.env)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(now), 0).In(loc), nil
}

func (s *Service) Home(ctx context.Context, in HomeInput) (application.Read[Home], error) {
	if in.View != "" && in.View != ArchivedThreads && in.View != AllThreads {
		return application.Read[Home]{}, invalid("view", "unknown view", nil)
	}
	in.Q = strings.TrimSpace(in.Q)
	day, err := s.day(ctx, s.now())
	if err != nil {
		return application.Read[Home]{}, err
	}
	var out Home
	rev, err := s.snapshot(ctx, string(HomeName), homeScopes, struct {
		HomeInput
		Minute int64
	}{in, day.Unix() / 60}, func(q db.Querier) error {
		if in.Agenda {
			agenda, err := s.agenda(ctx, q, day)
			out.Day = agenda
			return err
		}
		listed, err := listThreads(ctx, q, ThreadListInput{Q: in.Q, Archived: in.View == ArchivedThreads, Limit: in.Limit})
		if err != nil {
			return err
		}
		defaults, err := threads.Defaults(ctx, q)
		if err != nil {
			return err
		}
		out = Home{Threads: listed.Items, Q: in.Q, View: in.View, Defaults: defaults, Composer: DefaultComposer(ctx, defaults)}
		if in.List {
			return nil
		}
		out.Date = day.Format("Monday, January 2")
		if out.Digest, err = digestLines(ctx, q, day); err != nil {
			return err
		}
		out.Agenda = len(module.EnabledDaySources(ctx)) > 0
		if out.About, err = s.about(ctx, q, in.About); err != nil {
			return err
		}
		out.Welcome = len(out.Threads) == 0 && in.Q == "" && in.View == "" && out.About == nil
		if out.Welcome {
			exists, err := threads.HasChat(ctx, q)
			out.Welcome = !exists
			return err
		}
		return nil
	})
	return application.Read[Home]{Revision: rev, Data: out}, err
}

func digestLines(ctx context.Context, q db.Querier, day time.Time) ([]block.DigestLine, error) {
	var thread int64
	err := q.QueryRowContext(ctx,
		"SELECT id FROM threads WHERE kind = ? AND role = ? AND title = ? AND deleted_at IS NULL ORDER BY id LIMIT 1",
		threads.Chat, threads.TodayRole, threads.TodayTitle(day)).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT content_json FROM thread_messages WHERE thread_id = ? ORDER BY seq DESC LIMIT 20", thread)
	if err != nil {
		return nil, err
	}
	bodies, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var body string
		return body, r.Scan(&body)
	})
	if err != nil {
		return nil, err
	}
	for _, body := range bodies {
		var content []json.RawMessage
		if json.Unmarshal([]byte(body), &content) != nil {
			continue
		}
		if _, lines, ok, err := block.DigestLines(content); err == nil && ok {
			return lines, nil
		}
	}
	return nil, nil
}

func span(d time.Duration) string {
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return strconv.Itoa(m) + " min"
	case m == 0:
		return strconv.Itoa(h) + " h"
	}
	return strconv.Itoa(h) + " h " + strconv.Itoa(m) + " min"
}

func agendaOf(entries []day.Entry, now time.Time) []AgendaEvent {
	out := make([]AgendaEvent, 0, len(entries))
	for _, e := range entries {
		event := AgendaEvent{Time: "all day", Title: e.Title, Meta: e.Location, Ask: e.Ref}
		if !e.AllDay {
			event.Time = e.Start.Format("15:04")
			if e.End.After(e.Start) {
				event.Meta = strings.TrimPrefix(e.Location+" · "+span(e.End.Sub(e.Start)), " · ")
				switch {
				case !now.Before(e.End):
					event.State = "past"
				case !now.Before(e.Start):
					event.State = "now"
				}
			}
		}
		out = append(out, event)
	}
	return out
}

func (s *Service) agenda(ctx context.Context, q db.Querier, now time.Time) (*Agenda, error) {
	sources := module.EnabledDaySources(ctx)
	if len(sources) == 0 {
		return nil, notFound("no day source is on")
	}
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	entries, err := day.Collect(ctx, q, sources, from, from.AddDate(0, 0, 1))
	if err != nil {
		return nil, err
	}
	out := &Agenda{Events: agendaOf(entries, now)}
	for i, event := range out.Events {
		if event.State != "past" && event.Time != "all day" {
			out.Next = &out.Events[i]
			break
		}
	}
	return out, nil
}

func (s *Service) about(ctx context.Context, q db.Querier, ref string) (*About, error) {
	set := module.SetFrom(ctx)
	if set == nil {
		return nil, nil
	}
	kind, raw, _ := strings.Cut(ref, ":")
	resolver, ok := set.Catalog().Ref(kind)
	if !ok {
		return nil, nil
	}
	r, err := resolver.Resolve(ctx, q, raw)
	if err != nil || r == nil {
		return nil, err
	}
	return &About{Ref: r.Ref, Label: r.Label, Project: r.Project, Line: r.Line}, nil
}

func (s *Service) About(ctx context.Context, in AboutInput) (application.Read[*About], error) {
	var out *About
	rev, err := s.snapshot(ctx, string(AboutName), homeScopes, in, func(q db.Querier) error {
		var err error
		out, err = s.about(ctx, q, in.Ref)
		return err
	})
	return application.Read[*About]{Revision: rev, Data: out}, err
}

func (s *Service) TodayEnsure(ctx context.Context, _ TodayInput) (application.Change[TodayResult], error) {
	now := s.now()
	day, err := s.day(ctx, now)
	if err != nil {
		return application.Change[TodayResult]{}, err
	}
	var id, auditID int64
	err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
		var created bool
		if id, created, err = threads.TodayTx(ctx, tx, now, day); err != nil {
			return err
		}
		outcome := state.Noop
		if created {
			outcome = state.OK
		}
		auditID, err = s.audit(ctx, tx, TodayEnsureName, target("thread", id), outcome)
		return err
	})
	if err != nil {
		return application.Change[TodayResult]{}, err
	}
	return application.Change[TodayResult]{Data: TodayResult{ThreadID: id}, AuditID: auditID}, nil
}
