package calendartools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/assistant/registry/registrytest"
	"github.com/jeiang/atrium/internal/assistant/tool"
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const zone = "America/Port_of_Spain"

var clock = float64(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Unix())

type world struct {
	db     *db.DB
	tc     *tool.Context
	server *caldavtest.Server
}

func newWorld(t *testing.T, extra map[string]string) *world {
	t.Helper()
	server := caldavtest.New(t, "me@icloud.example", "app-secret")
	t.Cleanup(calendar.UseEndpoints(calendar.Endpoints{CalDAV: server.URL + "/"}))
	env := map[string]string{"ATRIUM_TZ": zone, "ICLOUD_APPLE_ID": "me@icloud.example", "ICLOUD_APP_PASSWORD": "app-secret"}
	for k, v := range extra {
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	d := dbtest.Schema(t)
	for _, q := range []string{
		"INSERT INTO threads (id, kind, effort, created_at, updated_at) VALUES (1, 'chat', 'medium', 0, 0)",
		"INSERT INTO runs (id, thread_id, kind, model, effort, state, started_at) VALUES (1, 1, 'chat', 'm', 'medium', 'running', 0)",
	} {
		if _, err := d.Write.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tc := &tool.Context{DB: d, Env: config.Map(env), Kind: tool.Chat, ThreadID: 1, RunID: 1, Now: func() float64 { return clock }}
	return &world{db: d, tc: tc, server: server}
}

func call(t *testing.T, w *world, name, args string) (py.Dict, error) {
	t.Helper()
	for _, tl := range calendartools.Tools() {
		if tl.Name != name {
			continue
		}
		in, err := py.JSONLoads([]byte(args))
		if err != nil {
			t.Fatal(err)
		}
		out, err := tl.Handler(context.Background(), w.tc, in.(py.Dict))
		if err != nil {
			return nil, err
		}
		return out.(py.Dict), nil
	}
	t.Fatalf("no tool %s", name)
	return nil, nil
}

func field(t *testing.T, d py.Dict, key string) any {
	t.Helper()
	v, ok := d.Get(key)
	if !ok {
		t.Fatalf("no %q in %s", key, py.JSONDumps(d))
	}
	return v
}

func TestToolsArePythonTools(t *testing.T) {
	tools := calendartools.Tools()
	if len(tools) != 4 {
		t.Fatalf("%d tools", len(tools))
	}
	r := registry.New()
	if err := r.Add(tools...); err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		registrytest.Verify(t, tl)
		if strings.Contains(string(tl.InputSchema), "attendees") {
			t.Errorf("%s accepts attendees", tl.Name)
		}
	}
}

func TestCreateDeleteAndUndoICloud(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	undoers := writes.NewRegistry()
	calendartools.RegisterUndoers(undoers)

	out, err := call(t, w, "calendar_create", `{"calendar":"icloud","title":"Focus","start":"2026-10-16T14:00","end":"2026-10-16T16:00","location":"Home","attendees":["a@b.test"]}`)
	if err != nil {
		t.Fatal(err)
	}
	uid := field(t, out, "uid").(string)
	if field(t, out, "title") != "Focus" || field(t, out, "start") != "2026-10-16T14:00:00-04:00" {
		t.Errorf("create result %s", py.JSONDumps(out))
	}
	var ics string
	for _, body := range w.server.Objects() {
		ics = body
	}
	for _, want := range []string{"UID:" + uid, "DTSTART:20261016T180000Z", "LOCATION:Home"} {
		if !strings.Contains(ics, want) {
			t.Errorf("ics lacks %q:\n%s", want, ics)
		}
	}
	if strings.Contains(ics, "ATTENDEE") || strings.Contains(ics, "ORGANIZER") {
		t.Errorf("attendees in ics:\n%s", ics)
	}
	if row, err := calendar.FindTracked(ctx, w.db.Write, uid); err != nil || row == nil || row.Calendar != "icloud" {
		t.Fatalf("tracked = %+v, %v", row, err)
	}

	removed, err := call(t, w, "calendar_delete", `{"uid":"`+uid+`"}`)
	if err != nil || field(t, removed, "deleted") != uid || len(w.server.Objects()) != 0 {
		t.Fatalf("delete = %v, %v, objects %v", removed, err, w.server.Objects())
	}
	_, err = call(t, w, "calendar_delete", `{"uid":"dentist-1"}`)
	var refused tool.Error
	if !errors.As(err, &refused) || !strings.Contains(refused.Error(), "created through atrium") {
		t.Errorf("deleting an event atrium did not create: %v", err)
	}

	var deleteWrite int64
	if err := w.db.Write.QueryRow("SELECT id FROM agent_writes WHERE tool = 'calendar_delete'").Scan(&deleteWrite); err != nil {
		t.Fatal(err)
	}
	res, err := undoers.Undo(ctx, w.db, deleteWrite, func() float64 { return clock })
	if err != nil {
		t.Fatal(err)
	}
	created := field(t, res.(py.Dict), "created").(string)
	if created == uid || len(w.server.Objects()) != 1 {
		t.Errorf("recreated %q (was %q), objects %v", created, uid, w.server.Objects())
	}

	var createWrite int64
	if err := w.db.Write.QueryRow("SELECT id FROM agent_writes WHERE tool = 'calendar_create'").Scan(&createWrite); err != nil {
		t.Fatal(err)
	}
	if _, err := undoers.Undo(ctx, w.db, createWrite, func() float64 { return clock }); err != nil {
		t.Fatal(err)
	}
	if row, err := calendar.FindTracked(ctx, w.db.Write, uid); err != nil || row != nil {
		t.Errorf("still tracked after undo: %+v, %v", row, err)
	}
}

func TestCreateValidation(t *testing.T) {
	w := newWorld(t, nil)
	for args, want := range map[string]string{
		`{"calendar":"outlook","title":"x","start":"2026-10-16"}`:                               "calendar must be one of icloud, google",
		`{"calendar":"icloud","title":"x","start":"2026-10-16T10:00","end":"2026-10-16T09:00"}`: "end must be after start",
		`{"calendar":"icloud","title":"x"}`:                                                     "start is required",
		`{"calendar":"icloud","title":"  ","start":"2026-10-16"}`:                               "title is required",
	} {
		_, err := call(t, w, "calendar_create", args)
		var te tool.Error
		if !errors.As(err, &te) || te.Error() != want {
			t.Errorf("%s: %v, want %q", args, err, want)
		}
	}
	bare := newWorld(t, map[string]string{"ICLOUD_APPLE_ID": "", "ICLOUD_APP_PASSWORD": ""})
	_, err := call(t, bare, "calendar_create", `{"calendar":"icloud","title":"x","start":"2026-10-16"}`)
	var missing tool.NotConfigured
	if !errors.As(err, &missing) || missing.Error() != "ICLOUD_APPLE_ID, ICLOUD_APP_PASSWORD not set" {
		t.Errorf("unconfigured iCloud: %v", err)
	}
	_, err = call(t, bare, "calendar_list", `{"start":"2026-10-16","end":"2026-10-17"}`)
	if !errors.As(err, &missing) || !strings.HasPrefix(missing.Error(), "ATRIUM_CALENDAR_DIR not set: iCloud mirror skipped; google: ") {
		t.Errorf("unconfigured list: %v", err)
	}
}

func TestProposeApproveAndUndo(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	if _, err := w.db.Write.Exec("INSERT INTO event_candidates (message_id, reason, status, ts) VALUES (7, 'invite', 'queued', 1)"); err != nil {
		t.Fatal(err)
	}
	args := `{"calendar":"icloud","title":"Project kickoff","start":"2026-11-04T11:00","end":"2026-11-04T12:00","location":"Room 4","source_message_id":7}`
	first, err := call(t, w, "event_propose", args)
	if err != nil || field(t, first, "proposed") != true {
		t.Fatalf("propose = %v, %v", first, err)
	}
	again, err := call(t, w, "event_propose", args)
	if err != nil || field(t, again, "proposed") != false || field(t, again, "reason") != "an identical proposal already exists" {
		t.Errorf("duplicate propose = %v, %v", again, err)
	}
	var status string
	if err := w.db.Write.QueryRow("SELECT status FROM event_candidates WHERE message_id = 7").Scan(&status); err != nil || status != "drafted" {
		t.Errorf("candidate status %q, %v", status, err)
	}
	if _, err := call(t, w, "event_propose", `{"calendar":"outlook","title":"x","start":"2026-11-04"}`); err == nil {
		t.Error("an unknown calendar was proposed")
	}

	id := field(t, first, "proposal_id").(int64)
	reg := proposals.NewRegistry()
	calendar.Register(reg, &calendar.Proposals{DB: w.db, Now: func() float64 { return clock }, Env: w.tc.Env})
	pipe := &proposals.Pipeline{DB: w.db, Registry: reg, Now: func() float64 { return clock }}
	done, err := pipe.ApproveAndApply(ctx, id, nil)
	if err != nil || done.Status != proposals.Applied {
		t.Fatalf("approve = %+v, %v", done, err)
	}
	if _, bad := done.Result.(py.Dict).Get("sync_error"); bad {
		t.Errorf("a calendar proposal ran a mail sync: %v", done.Result)
	}
	if len(w.server.Objects()) != 1 {
		t.Fatalf("objects %v", w.server.Objects())
	}
	for _, body := range w.server.Objects() {
		if strings.Contains(body, "ATTENDEE") || !strings.Contains(body, "SUMMARY:Project kickoff") {
			t.Errorf("event body:\n%s", body)
		}
	}
	if can, err := proposals.CanUndo(ctx, w.db.Write, id); err != nil || !can {
		t.Fatalf("can undo = %v, %v", can, err)
	}
	outcome, err := pipe.Undo(ctx, id)
	if err != nil || !outcome.Undone || len(w.server.Objects()) != 0 {
		t.Errorf("undo = %+v, %v, objects %v", outcome, err, w.server.Objects())
	}
}
