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
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/tracking"
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

var (
	aboutUID  = regexp.MustCompile(`^[A-Za-z0-9@._+-]{1,200}$`)
	aboutFile = regexp.MustCompile(`^[A-Za-z0-9._ ()+-]{1,200}$`)
)

func EventRef(day, uid string) string {
	if !aboutUID.MatchString(uid) {
		return ""
	}
	return "event:" + day + ":" + uid
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
		out.Agenda = module.IsEnabled(ctx, module.Calendar)
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

func agendaOf(rows []calendar.Row, tz *time.Location, now time.Time) []AgendaEvent {
	out := make([]AgendaEvent, 0, len(rows))
	for _, row := range rows {
		fields := row.Dict(tz)
		first, _ := fields["start"].(string)
		last, _ := fields["end"].(string)
		event := AgendaEvent{Time: "all day", Title: row.Title, Meta: row.Location, Ask: EventRef(now.Format(time.DateOnly), row.UID)}
		start, err := time.ParseInLocation("2006-01-02T15:04:05", first[:min(len(first), 19)], tz)
		end, endErr := time.ParseInLocation("2006-01-02T15:04:05", last[:min(len(last), 19)], tz)
		if err == nil {
			event.Time = start.Format("15:04")
			if endErr == nil && end.After(start) {
				event.Meta = strings.TrimPrefix(row.Location+" · "+span(end.Sub(start)), " · ")
				switch {
				case !now.Before(end):
					event.State = "past"
				case !now.Before(start):
					event.State = "now"
				}
			}
		}
		out = append(out, event)
	}
	return out
}

func (s *Service) agenda(ctx context.Context, q db.Querier, day time.Time) (*Agenda, error) {
	if !module.IsEnabled(ctx, module.Calendar) {
		return nil, notFound("calendar is off")
	}
	tz, err := calendar.Zone(ctx, s.env)
	if err != nil {
		return nil, err
	}
	set, err := calendar.LoadSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	now := day.In(tz)
	listing, err := calendar.ListEvents(ctx, s.env, set, now.Format(time.DateOnly), now.AddDate(0, 0, 1).Format(time.DateOnly), calendar.Filter{})
	if err != nil {
		return nil, &application.Error{Code: application.Unavailable, Message: "The calendar is unavailable.", Cause: err}
	}
	out := &Agenda{Events: agendaOf(listing.Events, tz, now)}
	for i, event := range out.Events {
		if event.State != "past" && event.Time != "all day" {
			out.Next = &out.Events[i]
			break
		}
	}
	return out, nil
}

func (s *Service) aboutEvent(ctx context.Context, q db.Querier, rest string) (*About, error) {
	day, uid, _ := strings.Cut(rest, ":")
	start, err := time.Parse(time.DateOnly, day)
	if err != nil || EventRef(day, uid) == "" || !module.IsEnabled(ctx, module.Calendar) {
		return nil, nil
	}
	set, err := calendar.LoadSettings(ctx, q)
	if err != nil {
		return nil, err
	}
	listing, err := calendar.ListEvents(ctx, s.env, set, day, start.AddDate(0, 0, 1).Format(time.DateOnly), calendar.Filter{})
	if err != nil {
		return nil, nil
	}
	for _, event := range listing.Events {
		if event.UID == uid {
			return &About{Ref: EventRef(day, uid), Label: event.Title, Line: "About the calendar event with uid " + uid + " on " + day + "."}, nil
		}
	}
	return nil, nil
}

func (s *Service) about(ctx context.Context, q db.Querier, ref string) (*About, error) {
	kind, raw, _ := strings.Cut(ref, ":")
	if kind == "event" {
		return s.aboutEvent(ctx, q, raw)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, nil
	}
	number := strconv.FormatInt(id, 10)
	a := About{Ref: kind + ":" + number}
	switch kind {
	case "mail":
		a.Label, err = mail.Subject(ctx, q, id)
		a.Line = "About mail message " + number + "."
	case "knowledge":
		a.Label, err = kb.Title(ctx, q, id)
		a.Line = "About knowledge record " + number + "."
	case "package":
		a.Label, err = tracking.Label(ctx, q, id)
		a.Line = "About tracked package " + number + "."
	case "file":
		a.Label, a.Project, err = files.ProjectFileName(ctx, q, id)
		if err == nil && !aboutFile.MatchString(a.Label) {
			return nil, nil
		}
		a.Line = "About the project file " + strconv.Quote(a.Label) + "."
	default:
		return nil, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.Label == "" {
		a.Label = strings.TrimSuffix(strings.TrimPrefix(a.Line, "About "), ".")
	}
	return &a, nil
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
