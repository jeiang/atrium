package calendar

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	gcal "google.golang.org/api/calendar/v3"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	DeleteOp   = "calendar_delete"
	UndeleteOp = "calendar_undelete"
)

type Event struct {
	Calendar  string `json:"calendar"`
	UID       string `json:"uid"`
	Title     string `json:"title"`
	Start     string `json:"start"`
	End       string `json:"end"`
	AllDay    bool   `json:"all_day"`
	Location  string `json:"location"`
	Notes     string `json:"notes"`
	Recurring bool   `json:"recurring"`
	Revision  string `json:"revision"`
	cancelled bool
}

type Missing string

func (e Missing) Error() string { return string(e) }

func (e Missing) Unwrap() error { return Error(string(e)) }

type Stale string

func (e Stale) Error() string { return string(e) }

type Blocked struct {
	ID       int64
	Calendar string
}

func (e Blocked) Error() string {
	return fmt.Sprintf("calendar write %d to this event has an unknown outcome; resolve it on the Activity page or with `atrium effects resolve %d --outcome applied` before writing to it again", e.ID, e.ID)
}

func revisionOf(e Event) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%s", e.Calendar, e.UID, e.Title, e.Start, e.End, e.AllDay, e.Location, e.Notes))
	return hex.EncodeToString(sum[:8])
}

func RevisionMatches(ifMatch, revision string) bool {
	for tag := range strings.SplitSeq(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.Trim(strings.TrimPrefix(tag, "W/"), `"`) == revision {
			return true
		}
	}
	return false
}

func icloudEvent(cal *ical.Calendar, uid string, tz *time.Location) (Event, error) {
	events, err := parseEvents(cal)
	if err != nil {
		return Event{}, err
	}
	var master *icsEvent
	for _, ev := range events {
		if ev.uidKey() == uid && (master == nil || master.recurrenceID != nil && ev.recurrenceID == nil) {
			master = ev
		}
	}
	if master == nil || master.dtstart == nil {
		return Event{}, Missing(fmt.Sprintf("no iCloud event with uid %q", uid))
	}
	start := *master.dtstart
	end, err := endOf(master, start, tz)
	if err != nil {
		return Event{}, err
	}
	e := Event{Calendar: ICloud, UID: uid, Title: master.summary, Start: fmtValue(start, tz), End: fmtValue(end, tz),
		AllDay: start.isDate(), Location: master.location, Notes: master.description, Recurring: len(master.rrules) > 0 || master.hasRDate}
	e.Revision = revisionOf(e)
	return e, nil
}

func googleEvent(ev *gcal.Event, tz *time.Location) (Event, error) {
	if ev.Start == nil || ev.End == nil {
		return Event{}, Error("the Google event has no start or end")
	}
	start, err := googleTime(ev.Start.DateTime, ev.Start.Date, tz)
	if err != nil {
		return Event{}, err
	}
	end, err := googleTime(ev.End.DateTime, ev.End.Date, tz)
	if err != nil {
		return Event{}, err
	}
	e := Event{Calendar: Google, UID: ev.Id, Title: ev.Summary, Start: fmtValue(start, tz), End: fmtValue(end, tz),
		AllDay: ev.Start.DateTime == "", Location: ev.Location, Notes: ev.Description,
		Recurring: len(ev.Recurrence) > 0 || ev.RecurringEventId != "", cancelled: ev.Status == "cancelled"}
	e.Revision = revisionOf(e)
	return e, nil
}

func specEvent(target Target, spec Spec, tz *time.Location) Event {
	e := Event{Calendar: string(target), UID: spec.UID, Title: spec.Title, Start: fmtValue(spec.Start, tz), End: fmtValue(spec.End, tz),
		AllDay: spec.AllDay, Location: spec.Location, Notes: spec.Notes}
	e.Revision = revisionOf(e)
	return e
}

func googleMissing(err error, uid string) error {
	if status := httpStatus(err); status == 404 || status == 410 {
		return Missing(fmt.Sprintf("no Google event with id %q", uid))
	}
	return remote(err)
}

func googleCurrent(ctx context.Context, svc *gcal.Service, uid string, tz *time.Location) (*gcal.Event, Event, error) {
	current, err := svc.Events.Get("primary", uid).Context(ctx).Do()
	if err != nil {
		return nil, Event{}, googleMissing(err, uid)
	}
	event, err := googleEvent(current, tz)
	if err != nil {
		return nil, Event{}, err
	}
	if event.cancelled {
		return nil, Event{}, Missing(fmt.Sprintf("no Google event with id %q", uid))
	}
	return current, event, nil
}

func ReadEvent(ctx context.Context, env config.Env, set Settings, target Target, uid string, tz *time.Location) (Event, error) {
	switch target {
	case ICloud:
		obj, err := findICloud(ctx, env, set, uid)
		if err != nil {
			return Event{}, missingICloud(err)
		}
		return icloudEvent(obj.cal, uid, tz)
	case Google:
		svc, err := googleService(ctx, env, set)
		if err != nil {
			return Event{}, err
		}
		_, event, err := googleCurrent(ctx, svc, uid, tz)
		return event, err
	}
	return Event{}, targetError()
}

func missingICloud(err error) error {
	if text := err.Error(); strings.HasPrefix(text, "no iCloud event with uid") {
		return Missing(text)
	}
	return err
}

func (p *Proposals) blocked(ctx context.Context, target Target, uid string) error {
	id, err := ledger.BlockedTarget(ctx, p.DB.Read, string(target)+":"+uid)
	if err != nil {
		return err
	}
	if id != 0 {
		return Blocked{ID: id, Calendar: string(target)}
	}
	return nil
}

func (p *Proposals) CreateDirect(ctx context.Context, target Target, raw map[string]any) (Event, int64, error) {
	env := p.env()
	tz, err := Zone(ctx, env)
	if err != nil {
		return Event{}, 0, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return Event{}, 0, err
	}
	fields := maps.Clone(raw)
	delete(fields, "uid")
	spec, err := NormalizeSpec(fields, tz, "", set.DefaultMinutes)
	if err != nil {
		return Event{}, 0, err
	}
	if err := p.blocked(ctx, target, spec.UID); err != nil {
		return Event{}, 0, err
	}
	action := actionPayload{Type: Kind, Calendar: string(target), UID: spec.UID}
	undo := undoPlan{Op: UndoOp, Calendar: string(target), UID: spec.UID}
	ticket, err := p.begin(ctx, nil, Kind, target, spec.UID, action, nil, undo)
	if err != nil {
		return Event{}, 0, err
	}
	if _, err := CreateEvent(ctx, env, set, target, spec, tz); err != nil {
		return Event{}, ticket.ID, p.fail(ctx, ticket, err)
	}
	if _, err := p.settleRecord(ctx, ticket, action, undo, ""); err != nil {
		return Event{}, ticket.ID, err
	}
	return specEvent(target, spec, tz), ticket.ID, nil
}

func (p *Proposals) UpdateDirect(ctx context.Context, target Target, uid, ifMatch string, changes map[string]any) (Event, int64, error) {
	if err := RefuseBirthday(uid); err != nil {
		return Event{}, 0, err
	}
	env := p.env()
	tz, err := Zone(ctx, env)
	if err != nil {
		return Event{}, 0, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return Event{}, 0, err
	}
	if err := p.blocked(ctx, target, uid); err != nil {
		return Event{}, 0, err
	}
	var before Event
	var spec Spec
	edit := func(current func() (Event, error)) (Spec, error) {
		event, err := current()
		if err != nil {
			return Spec{}, err
		}
		if event.cancelled {
			return Spec{}, Missing(fmt.Sprintf("no Google event with id %q", uid))
		}
		if !RevisionMatches(ifMatch, event.Revision) {
			return Spec{}, Stale("the event changed since it was read; reload it and try again")
		}
		before = event
		merged := map[string]any{"title": event.Title, "start": event.Start, "end": event.End, "all_day": event.AllDay, "location": event.Location, "notes": event.Notes}
		if allDay, ok := changes["all_day"].(bool); ok && allDay != event.AllDay && (changes["start"] == nil || changes["end"] == nil) {
			return Spec{}, Error("give start and end when changing all_day")
		}
		maps.Copy(merged, changes)
		spec, err = NormalizeSpec(merged, tz, uid, set.DefaultMinutes)
		return spec, err
	}
	var plan func() (undoPlan, error)
	switch target {
	case ICloud:
		plan, err = planICloudUpdate(ctx, env, set, uid, tz, edit)
		err = missingICloud(err)
	case Google:
		plan, err = planGoogleUpdate(ctx, env, set, uid, tz, edit)
	default:
		err = targetError()
	}
	if err != nil {
		return Event{}, 0, err
	}
	action := actionPayload{Type: UpdateKind, Calendar: string(target), UID: uid}
	ticket, err := p.begin(ctx, nil, UpdateKind, target, uid, action, before, nil)
	if err != nil {
		return Event{}, 0, err
	}
	undo, err := plan()
	if err != nil {
		return Event{}, ticket.ID, p.fail(ctx, ticket, err)
	}
	written := specEvent(target, spec, tz)
	if undo.after != nil {
		written = *undo.after
		undo.Revision = written.Revision
	}
	if _, err := p.settleRecord(ctx, ticket, action, undo, ""); err != nil {
		return Event{}, ticket.ID, err
	}
	return written, ticket.ID, nil
}

func googleKept(ev *gcal.Event) (string, error) {
	raw, err := json.Marshal(&gcal.Event{Summary: ev.Summary, Location: ev.Location, Description: ev.Description, Start: ev.Start, End: ev.End,
		Attendees: ev.Attendees, Recurrence: ev.Recurrence, Status: "confirmed"})
	return string(raw), err
}

func (p *Proposals) DeleteDirect(ctx context.Context, target Target, uid, ifMatch string) (int64, error) {
	if err := RefuseBirthday(uid); err != nil {
		return 0, err
	}
	env := p.env()
	tz, err := Zone(ctx, env)
	if err != nil {
		return 0, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return 0, err
	}
	if err := p.blocked(ctx, target, uid); err != nil {
		return 0, err
	}
	var before Event
	var undo undoPlan
	var remove func() error
	switch target {
	case ICloud:
		obj, err := findICloud(ctx, env, set, uid)
		if err != nil {
			return 0, missingICloud(err)
		}
		if before, err = icloudEvent(obj.cal, uid, tz); err != nil {
			return 0, err
		}
		undo = undoPlan{Op: UndeleteOp, Calendar: ICloud, UID: uid, ICS: obj.raw, Path: obj.path.Path}
		remove = func() error { return remote(deleteObject(ctx, obj.client, obj.path)) }
	case Google:
		svc, err := googleService(ctx, env, set)
		if err != nil {
			return 0, err
		}
		current, event, err := googleCurrent(ctx, svc, uid, tz)
		if err != nil {
			return 0, err
		}
		kept, err := googleKept(current)
		if err != nil {
			return 0, err
		}
		before = event
		undo = undoPlan{Op: UndeleteOp, Calendar: Google, UID: uid, Google: kept}
		remove = func() error { return googleDelete(ctx, svc, uid) }
	default:
		return 0, targetError()
	}
	if ifMatch != "" && !RevisionMatches(ifMatch, before.Revision) {
		return 0, Stale("the event changed since it was read; reload it and try again")
	}
	action := actionPayload{Type: DeleteOp, Calendar: string(target), UID: uid}
	ticket, err := p.begin(ctx, nil, DeleteOp, target, uid, action, before, undo)
	if err != nil {
		return 0, err
	}
	if err := remove(); err != nil {
		return ticket.ID, p.fail(ctx, ticket, err)
	}
	after, err := encode(action)
	if err != nil {
		return ticket.ID, err
	}
	plan, err := encode(undo)
	if err != nil {
		return ticket.ID, err
	}
	err = p.Ledger.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Done, After: after, Undo: plan},
		func(ctx context.Context, tx *sql.Tx) error { return Untrack(ctx, tx, uid, p.now()) })
	return ticket.ID, err
}

func (p *Proposals) Undelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var info undoPlan
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	target, err := ParseTarget(info.Calendar)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target, info.UID); err != nil {
		return nil, err
	}
	switch target {
	case ICloud:
		client, collections, err := icloudCalendars(ctx, p.env(), set)
		if err != nil {
			return nil, err
		}
		var path *url.URL
		for _, collection := range collections {
			if strings.HasPrefix(info.Path, strings.TrimRight(collection.URL.Path, "/")+"/") {
				u := *collection.URL
				u.Path, u.RawPath = info.Path, ""
				path = &u
				break
			}
		}
		if path == nil {
			return nil, Error("the iCloud calendar that held this event is gone")
		}
		if err := client.Put(ctx, path, []byte(info.ICS), "text/calendar; charset=utf-8"); err != nil {
			return nil, remote(err)
		}
	case Google:
		svc, err := googleService(ctx, p.env(), set)
		if err != nil {
			return nil, err
		}
		var body gcal.Event
		if err := json.Unmarshal([]byte(info.Google), &body); err != nil {
			return nil, err
		}
		if _, err := svc.Events.Patch("primary", info.UID, &body).SendUpdates("none").Context(ctx).Do(); err != nil {
			return nil, remote(err)
		}
	}
	return RestoreResult{Restored: info.UID}, nil
}
