package calendarmodule

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/jobworker"
	calapp "github.com/jeiang/atrium/internal/calendar/application"
	"github.com/jeiang/atrium/internal/calendar/candidates"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/safety"
	"github.com/jeiang/atrium/internal/scheduler"
	"github.com/jeiang/atrium/internal/web"
)

type EventsRequest struct {
	From     string `query:"from" json:"-"`
	To       string `query:"to" json:"-"`
	Calendar string `query:"calendar" json:"-"`
}

type Event struct {
	UID      string `json:"uid"`
	Calendar string `json:"calendar"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Title    string `json:"title"`
	Location string `json:"location"`
}

type Events struct {
	Events    []Event  `json:"events"`
	Truncated bool     `json:"truncated"`
	Notes     []string `json:"notes"`
}

type FreeSlotsRequest struct {
	From     string `query:"from" json:"-"`
	To       string `query:"to" json:"-"`
	Minutes  int64  `query:"duration_minutes" json:"-" api:"min=1"`
	Calendar string `query:"calendar" json:"-"`
}

type Window struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type FreeSlots struct {
	Windows []Window `json:"windows"`
	Notes   []string `json:"notes"`
}

type Candidates struct {
	Candidates int    `json:"candidates"`
	RunID      *int64 `json:"run_id" api:"nullable"`
	Proposed   int    `json:"proposed"`
}

func retype[T any](v map[string]any) (T, error) {
	var out T
	raw, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}

func (m Module) calendarRoutes(d *web.Deps) []application.Binding {
	reads := func() *calapp.Service { return calapp.New(d.DB, d.Env, nil, assistant.Now) }
	return []application.Binding{
		api.Query(d, calapp.Describe()[0], application.APIShape{Area: "home", Errors: []application.Code{application.Invalid}},
			"GET /calendar/events", func(ctx context.Context, in EventsRequest) (application.Read[Events], error) {
				read, err := reads().List(ctx, calapp.ListInput{Start: in.From, End: in.To, Calendar: in.Calendar})
				if err != nil {
					return application.Read[Events]{}, err
				}
				out, err := retype[Events](read.Data.Value)
				return application.Read[Events]{Revision: read.Revision, Data: out}, err
			}),
		api.Query(d, calapp.Describe()[1], application.APIShape{Area: "home", Errors: []application.Code{application.Invalid}},
			"GET /calendar/free-slots", func(ctx context.Context, in FreeSlotsRequest) (application.Read[FreeSlots], error) {
				read, err := reads().FindSlot(ctx, calapp.FindSlotInput{Start: in.From, End: in.To, Minutes: in.Minutes, Calendar: in.Calendar})
				if err != nil {
					return application.Read[FreeSlots]{}, err
				}
				out, err := retype[FreeSlots](read.Data.Value)
				return application.Read[FreeSlots]{Revision: read.Revision, Data: out}, err
			}),
		api.Command(d, calapp.DescribePasses()[0], application.APIShape{Area: "integrations", Errors: []application.Code{application.NotConfigured, application.Unavailable, application.Conflict}},
			"POST /calendar/sync", func(ctx context.Context, in calapp.SyncInput) (application.Change[davmirror.Stats], error) {
				change, err := reads().SyncManual(ctx, d.Cfg)
				return change, syncBusy(err, "A calendar sync is already running.")
			}),
		api.Command(d, calapp.DescribePasses()[1], application.APIShape{Area: "integrations", Errors: []application.Code{application.NotConfigured, application.Unavailable, application.Conflict, application.Refused}},
			"POST /calendar/candidates", func(ctx context.Context, in calapp.DraftCandidatesInput) (application.Change[Candidates], error) {
				out, err := m.draftNow(ctx, d, in)
				if err != nil {
					return application.Change[Candidates]{}, err
				}
				c := Candidates{Candidates: out.Data.Candidates, Proposed: out.Data.Proposed}
				if out.Data.RunID != 0 {
					c.RunID = &out.Data.RunID
				}
				return application.Change[Candidates]{Data: c, EffectIDs: out.EffectIDs, AuditID: out.AuditID}, nil
			}),
	}
}

func syncBusy(err error, message string) error {
	if errors.As(err, new(*db.LockBusy)) {
		return &application.Error{Code: application.Conflict, Message: message, Cause: err}
	}
	return err
}

func (m Module) draftNow(ctx context.Context, d *web.Deps, in calapp.DraftCandidatesInput) (application.Change[candidates.Result], error) {
	def := CandidatesJob(m.Jobs)
	for _, need := range append([]string{def.Module}, def.Needs...) {
		if !module.IsEnabled(ctx, need) {
			return application.Change[candidates.Result]{}, &application.Error{Code: application.Refused, Message: "Event candidates need the " + need + " module, which is off."}
		}
	}
	if !def.RunsWhilePaused && safety.IsPaused(d.Cfg.PausePath()) {
		return application.Change[candidates.Result]{}, &application.Error{Code: application.Refused, Message: "atrium is paused."}
	}
	var out application.Change[candidates.Result]
	var draftErr error
	run := m.Jobs.Callable(func(ctx context.Context, e jobworker.Env) (jobworker.Result, error) {
		out, draftErr = draftCandidates(ctx, e, in)
		return jobworker.Result{RunID: out.Data.RunID}, draftErr
	})
	_, err := scheduler.RunNow(ctx, d.DB, d.Cfg.DBPath, config.From(ctx).TZName(d.Env), def, db.ShortLockWait, run)
	if err == nil {
		err = draftErr
	}
	return out, syncBusy(err, "Event candidates are already being drafted.")
}
