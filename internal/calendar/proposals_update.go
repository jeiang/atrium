package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/caldav"
	gcal "google.golang.org/api/calendar/v3"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/proposals"
	"github.com/jeiang/atrium/internal/py"
)

const (
	UpdateKind   = "calendar_update"
	RSVPKind     = "calendar_rsvp"
	RestoreOp    = "calendar_restore"
	Accepted     = "accepted"
	Declined     = "declined"
	Tentative    = "tentative"
	partstatKind = "PARTSTAT"
)

var Responses = []string{Accepted, Declined, Tentative}

var icsPartstat = map[string]string{Accepted: "ACCEPTED", Declined: "DECLINED", Tentative: "TENTATIVE"}

type icloudObject struct {
	client *caldav.Client
	path   string
	cal    *ical.Calendar
	raw    string
}

func encodeICS(cal *ical.Calendar) (string, error) {
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func findICloud(ctx context.Context, env config.Env, set Settings, uid string) (*icloudObject, error) {
	client, calendars, err := icloudCalendars(ctx, env, set)
	if err != nil {
		return nil, err
	}
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid}}},
		}}},
	}
	for _, c := range calendars {
		objects, err := client.QueryCalendar(ctx, c.Path, query)
		if err != nil {
			return nil, remote(err)
		}
		for _, o := range objects {
			if hasUID(o.Data, uid) {
				raw, err := encodeICS(o.Data)
				if err != nil {
					return nil, err
				}
				return &icloudObject{client: client, path: o.Path, cal: o.Data, raw: raw}, nil
			}
		}
	}
	return nil, Error(fmt.Sprintf("no iCloud event with uid %s", py.Repr(uid)))
}

func (o *icloudObject) put(ctx context.Context, cal *ical.Calendar) error {
	_, err := o.client.PutCalendarObject(ctx, o.path, cal)
	return remote(err)
}

func eventOf(cal *ical.Calendar) *ical.Component {
	var first *ical.Component
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if c.Props.Get(ical.PropRecurrenceID) == nil {
			return c
		}
		if first == nil {
			first = c
		}
	}
	return first
}

var updatedProps = []string{ical.PropSummary, ical.PropDateTimeStart, ical.PropDateTimeEnd, ical.PropLocation, ical.PropDescription, ical.PropDateTimeStamp}

func updateICloud(ctx context.Context, env config.Env, set Settings, spec Spec, tz *time.Location) (py.Dict, error) {
	obj, err := findICloud(ctx, env, set, spec.UID)
	if err != nil {
		return nil, err
	}
	copied, err := decodeICS([]byte(obj.raw))
	if err != nil {
		return nil, err
	}
	fresh, err := buildCalendar(spec, tz, time.Now())
	if err != nil {
		return nil, err
	}
	ev, want := eventOf(copied), eventOf(fresh)
	if ev == nil || want == nil {
		return nil, Error("the iCloud object has no event")
	}
	for _, name := range updatedProps {
		if props, ok := want.Props[name]; ok {
			ev.Props[name] = props
		}
	}
	delete(ev.Props, ical.PropDuration)
	sequence := 0
	if p := ev.Props.Get(ical.PropSequence); p != nil {
		sequence, _ = strconv.Atoi(strings.TrimSpace(p.Value))
	}
	ev.Props.Set(&ical.Prop{Name: ical.PropSequence, Params: ical.Params{}, Value: strconv.Itoa(sequence + 1)})
	if err := obj.put(ctx, copied); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "op", Value: RestoreOp}, {Key: "calendar", Value: ICloud}, {Key: "uid", Value: spec.UID}, {Key: "ics", Value: obj.raw}}, nil
}

func rsvpICloud(ctx context.Context, env config.Env, set Settings, uid, response string) (py.Dict, error) {
	obj, err := findICloud(ctx, env, set, uid)
	if err != nil {
		return nil, err
	}
	copied, err := decodeICS([]byte(obj.raw))
	if err != nil {
		return nil, err
	}
	ev := eventOf(copied)
	if ev == nil {
		return nil, Error("the iCloud object has no event")
	}
	self := "mailto:" + strings.ToLower(set.AppleID)
	found := false
	for i, a := range ev.Props[ical.PropAttendee] {
		if strings.ToLower(a.Value) == self {
			ev.Props[ical.PropAttendee][i].Params.Set(partstatKind, icsPartstat[response])
			ev.Props[ical.PropAttendee][i].Params.Del("RSVP")
			found = true
		}
	}
	if !found {
		return nil, Error("the operator is not an attendee of this event")
	}
	if err := obj.put(ctx, copied); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "op", Value: RestoreOp}, {Key: "calendar", Value: ICloud}, {Key: "uid", Value: uid}, {Key: "ics", Value: obj.raw}}, nil
}

func googleSnapshot(ev *gcal.Event) (string, error) {
	raw, err := json.Marshal(&gcal.Event{Summary: ev.Summary, Location: ev.Location, Description: ev.Description, Start: ev.Start, End: ev.End, Attendees: ev.Attendees})
	return string(raw), err
}

func updateGoogle(ctx context.Context, env config.Env, set Settings, spec Spec, tz *time.Location) (py.Dict, error) {
	svc, err := googleService(ctx, env, set)
	if err != nil {
		return nil, err
	}
	current, err := svc.Events.Get("primary", spec.UID).Context(ctx).Do()
	if err != nil {
		return nil, remote(err)
	}
	snapshot, err := googleSnapshot(current)
	if err != nil {
		return nil, err
	}
	body, err := GoogleBody(spec, tz)
	if err != nil {
		return nil, err
	}
	body.Id, body.Attendees = "", nil
	if _, err := svc.Events.Patch("primary", spec.UID, body).SendUpdates("none").Context(ctx).Do(); err != nil {
		return nil, remote(err)
	}
	return py.Dict{{Key: "op", Value: RestoreOp}, {Key: "calendar", Value: Google}, {Key: "uid", Value: spec.UID}, {Key: "google", Value: snapshot}}, nil
}

func rsvpGoogle(ctx context.Context, env config.Env, set Settings, uid, response string) (py.Dict, error) {
	svc, err := googleService(ctx, env, set)
	if err != nil {
		return nil, err
	}
	current, err := svc.Events.Get("primary", uid).Context(ctx).Do()
	if err != nil {
		return nil, remote(err)
	}
	snapshot, err := googleSnapshot(current)
	if err != nil {
		return nil, err
	}
	attendees := slices.Clone(current.Attendees)
	found := false
	for i, a := range attendees {
		if a.Self {
			copied := *a
			copied.ResponseStatus = response
			attendees[i] = &copied
			found = true
		}
	}
	if !found {
		return nil, Error("the operator is not an attendee of this event")
	}
	if _, err := svc.Events.Patch("primary", uid, &gcal.Event{Attendees: attendees}).SendUpdates("none").Context(ctx).Do(); err != nil {
		return nil, remote(err)
	}
	return py.Dict{{Key: "op", Value: RestoreOp}, {Key: "calendar", Value: Google}, {Key: "uid", Value: uid}, {Key: "google", Value: snapshot}}, nil
}

func targetOf(raw py.Dict) (string, error) {
	chosen, _ := raw.Get("calendar")
	s, ok := chosen.(string)
	if !ok || s != ICloud && s != Google {
		return "", ProposalError("calendar must be one of " + TargetsText)
	}
	return s, nil
}

func uidOf(raw py.Dict) (string, error) {
	v, _ := raw.Get("uid")
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", ProposalError("uid is required")
	}
	return s, nil
}

func (p *Proposals) ProposeUpdate(ctx context.Context, raw py.Dict) (int64, bool, error) {
	if _, found := raw.Get("attendees"); found {
		return 0, false, ProposalError("attendees cannot be changed")
	}
	target, err := targetOf(raw)
	if err != nil {
		return 0, false, err
	}
	uid, err := uidOf(raw)
	if err != nil {
		return 0, false, err
	}
	tz, err := Zone(p.env())
	if err != nil {
		return 0, false, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return 0, false, err
	}
	spec, err := NormalizeSpec(raw, tz, uid, set.DefaultMinutes)
	if err != nil {
		if _, ok := err.(Error); ok {
			return 0, false, ProposalError(err.Error())
		}
		return 0, false, err
	}
	payload := py.Dict{{Key: "calendar", Value: target}, {Key: "spec", Value: spec.Dict()}}
	preview := py.Dict{{Key: "calendar", Value: target}, {Key: "uid", Value: uid}, {Key: "title", Value: spec.Title},
		{Key: "start", Value: spec.Start}, {Key: "end", Value: spec.End}, {Key: "location", Value: spec.Location}}
	account := Account
	return proposals.Create(ctx, p.DB, UpdateKind, &account, "Change calendar event: "+spec.Title, payload, preview,
		fingerprint(UpdateKind, target, spec.Dict()), p.now())
}

func (p *Proposals) ProposeRSVP(ctx context.Context, raw py.Dict) (int64, bool, error) {
	target, err := targetOf(raw)
	if err != nil {
		return 0, false, err
	}
	uid, err := uidOf(raw)
	if err != nil {
		return 0, false, err
	}
	v, _ := raw.Get("response")
	response, _ := v.(string)
	if !slices.Contains(Responses, response) {
		return 0, false, ProposalError("response must be one of " + strings.Join(Responses, ", "))
	}
	title, _ := raw.Get("title")
	label, _ := title.(string)
	if label == "" {
		label = uid
	}
	payload := py.Dict{{Key: "calendar", Value: target}, {Key: "uid", Value: uid}, {Key: "response", Value: response}}
	account := Account
	return proposals.Create(ctx, p.DB, RSVPKind, &account, fmt.Sprintf("Reply %s: %s", response, label), payload, payload,
		fingerprint(RSVPKind, target, uid, response), p.now())
}

func (p *Proposals) record(ctx context.Context, prop *proposals.Proposal, kind string, undo py.Dict, summary string) (py.Dict, error) {
	calendarName, _ := undo.Get("calendar")
	uid, _ := undo.Get("uid")
	action := py.Dict{{Key: "type", Value: kind}, {Key: "calendar", Value: calendarName}, {Key: "uid", Value: uid}}
	if _, err := proposals.RecordAction(ctx, p.DB.Write, prop.ID, action, undo, true, nil, p.now()); err != nil {
		return nil, err
	}
	return py.Dict{{Key: "summary", Value: summary}}, nil
}

func (p *Proposals) ExecuteUpdate(ctx context.Context, prop *proposals.Proposal, _ any) (py.Dict, error) {
	target, spec, err := payloadOf(prop)
	if err != nil {
		return nil, err
	}
	env := p.env()
	tz, err := Zone(env)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	var undo py.Dict
	switch target {
	case ICloud:
		undo, err = updateICloud(ctx, env, set, spec, tz)
	case Google:
		undo, err = updateGoogle(ctx, env, set, spec, tz)
	default:
		err = targetError()
	}
	if err != nil {
		return nil, err
	}
	return p.record(ctx, prop, UpdateKind, undo, "Changed the event on the "+target+" calendar")
}

func (p *Proposals) ExecuteRSVP(ctx context.Context, prop *proposals.Proposal, _ any) (py.Dict, error) {
	var info struct {
		Calendar string `json:"calendar"`
		UID      string `json:"uid"`
		Response string `json:"response"`
	}
	if err := json.Unmarshal([]byte(prop.Payload), &info); err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	var undo py.Dict
	switch info.Calendar {
	case ICloud:
		undo, err = rsvpICloud(ctx, p.env(), set, info.UID, info.Response)
	case Google:
		undo, err = rsvpGoogle(ctx, p.env(), set, info.UID, info.Response)
	default:
		err = targetError()
	}
	if err != nil {
		return nil, err
	}
	return p.record(ctx, prop, RSVPKind, undo, "Replied "+info.Response)
}

func (p *Proposals) Restore(ctx context.Context, plan json.RawMessage) (py.Dict, error) {
	var info struct {
		Calendar string `json:"calendar"`
		UID      string `json:"uid"`
		ICS      string `json:"ics"`
		Google   string `json:"google"`
	}
	if err := json.Unmarshal(plan, &info); err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	switch info.Calendar {
	case ICloud:
		obj, err := findICloud(ctx, p.env(), set, info.UID)
		if err != nil {
			return nil, err
		}
		original, err := decodeICS([]byte(info.ICS))
		if err != nil {
			return nil, err
		}
		if err := obj.put(ctx, original); err != nil {
			return nil, err
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
	default:
		return nil, targetError()
	}
	return py.Dict{{Key: "restored", Value: info.UID}}, nil
}
