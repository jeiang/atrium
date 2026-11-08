package calendar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	gcal "google.golang.org/api/calendar/v3"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
	"github.com/jeiang/atrium/internal/proposals"
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

type updatePayload struct {
	Calendar string      `json:"calendar"`
	Spec     specPayload `json:"spec"`
}

type updatePreview struct {
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location"`
}

type rsvpPayload struct {
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	Response string `json:"response"`
}

type icloudObject struct {
	client *davmirror.Client
	path   *url.URL
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
	client, collections, err := icloudCalendars(ctx, env, set)
	if err != nil {
		return nil, err
	}
	for _, collection := range collections {
		objects, err := client.Query(ctx, collection.URL, davmirror.Calendars, uid)
		if err != nil {
			return nil, remote(err)
		}
		for _, object := range objects {
			cal, err := decodeICS(object.Body)
			if err != nil || !hasUID(cal, uid) {
				continue
			}
			raw, err := encodeICS(cal)
			if err != nil {
				return nil, err
			}
			return &icloudObject{client: client, path: object.URL, cal: cal, raw: raw}, nil
		}
	}
	return nil, Error(fmt.Sprintf("no iCloud event with uid %q", uid))
}

func (o *icloudObject) put(ctx context.Context, cal *ical.Calendar) error {
	body, err := encodeICS(cal)
	if err != nil {
		return err
	}
	return remote(o.client.Put(ctx, o.path, []byte(body), "text/calendar; charset=utf-8"))
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

func planICloudUpdate(ctx context.Context, env config.Env, set Settings, spec Spec, tz *time.Location) (func() (undoPlan, error), error) {
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
		} else if name == ical.PropLocation || name == ical.PropDescription {
			delete(ev.Props, name)
		}
	}
	delete(ev.Props, ical.PropDuration)
	sequence := 0
	if p := ev.Props.Get(ical.PropSequence); p != nil {
		sequence, _ = strconv.Atoi(strings.TrimSpace(p.Value))
	}
	ev.Props.Set(&ical.Prop{Name: ical.PropSequence, Params: ical.Params{}, Value: strconv.Itoa(sequence + 1)})
	return func() (undoPlan, error) {
		if err := obj.put(ctx, copied); err != nil {
			return undoPlan{}, err
		}
		return undoPlan{Op: RestoreOp, Calendar: string(ICloud), UID: spec.UID, ICS: obj.raw}, nil
	}, nil
}

func planICloudRSVP(ctx context.Context, env config.Env, set Settings, uid, response string) (func() (undoPlan, error), error) {
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
	return func() (undoPlan, error) {
		if err := obj.put(ctx, copied); err != nil {
			return undoPlan{}, err
		}
		return undoPlan{Op: RestoreOp, Calendar: string(ICloud), UID: uid, ICS: obj.raw}, nil
	}, nil
}

func googleSnapshot(ev *gcal.Event) (string, error) {
	raw, err := json.Marshal(&gcal.Event{Summary: ev.Summary, Location: ev.Location, Description: ev.Description, Start: ev.Start, End: ev.End, Attendees: ev.Attendees})
	return string(raw), err
}

func planGoogleUpdate(ctx context.Context, env config.Env, set Settings, spec Spec, tz *time.Location) (func() (undoPlan, error), error) {
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
	body.ForceSendFields = []string{"Location", "Description"}
	return func() (undoPlan, error) {
		if _, err := svc.Events.Patch("primary", spec.UID, body).SendUpdates("none").Context(ctx).Do(); err != nil {
			return undoPlan{}, remote(err)
		}
		return undoPlan{Op: RestoreOp, Calendar: string(Google), UID: spec.UID, Google: snapshot}, nil
	}, nil
}

func planGoogleRSVP(ctx context.Context, env config.Env, set Settings, uid, response string) (func() (undoPlan, error), error) {
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
	return func() (undoPlan, error) {
		if _, err := svc.Events.Patch("primary", uid, &gcal.Event{Attendees: attendees}).SendUpdates("none").Context(ctx).Do(); err != nil {
			return undoPlan{}, remote(err)
		}
		return undoPlan{Op: RestoreOp, Calendar: string(Google), UID: uid, Google: snapshot}, nil
	}, nil
}

func targetOf(raw map[string]any) (Target, error) {
	s, ok := raw["calendar"].(string)
	if !ok {
		return "", ProposalError("calendar must be one of " + TargetsText)
	}
	target, err := ParseTarget(s)
	if err != nil {
		return "", ProposalError(err.Error())
	}
	return target, nil
}

func uidOf(raw map[string]any) (string, error) {
	s, ok := raw["uid"].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", ProposalError("uid is required")
	}
	return s, nil
}

func (p *Proposals) ProposeUpdate(ctx context.Context, raw map[string]any) (int64, bool, error) {
	if _, found := raw["attendees"]; found {
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
	tz, err := Zone(ctx, p.env())
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
	payload := updatePayload{Calendar: string(target), Spec: spec.payload()}
	preview := updatePreview{Calendar: string(target), UID: uid, Title: spec.Title,
		Start: spec.Start.iso(), End: spec.End.iso(), Location: spec.Location}
	account := Account
	return proposals.Create(ctx, p.DB, UpdateKind, &account, "Change calendar event: "+spec.Title, payload, preview,
		fingerprint(UpdateKind, target, spec.payload()), p.now())
}

func (p *Proposals) ProposeRSVP(ctx context.Context, raw map[string]any) (int64, bool, error) {
	target, err := targetOf(raw)
	if err != nil {
		return 0, false, err
	}
	uid, err := uidOf(raw)
	if err != nil {
		return 0, false, err
	}
	response, _ := raw["response"].(string)
	if !slices.Contains(Responses, response) {
		return 0, false, ProposalError("response must be one of " + strings.Join(Responses, ", "))
	}
	label, _ := raw["title"].(string)
	if label == "" {
		label = uid
	}
	payload := rsvpPayload{Calendar: string(target), UID: uid, Response: response}
	account := Account
	return proposals.Create(ctx, p.DB, RSVPKind, &account, fmt.Sprintf("Reply %s: %s", response, label), payload, payload,
		fingerprint(RSVPKind, target, uid, response), p.now())
}

func (p *Proposals) ExecuteUpdate(ctx context.Context, prop *proposals.Proposal, _ any) (any, error) {
	target, spec, err := payloadOf(prop)
	if err != nil {
		return nil, err
	}
	env := p.env()
	tz, err := Zone(ctx, env)
	if err != nil {
		return nil, err
	}
	set, err := LoadSettings(ctx, p.DB.Read)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target); err != nil {
		return nil, err
	}
	var plan func() (undoPlan, error)
	switch target {
	case ICloud:
		plan, err = planICloudUpdate(ctx, env, set, spec, tz)
	case Google:
		plan, err = planGoogleUpdate(ctx, env, set, spec, tz)
	default:
		err = targetError()
	}
	if err != nil {
		return nil, err
	}
	action := actionPayload{Type: UpdateKind, Calendar: string(target), UID: spec.UID}
	intent, err := p.begin(ctx, prop, action)
	if err != nil {
		return nil, err
	}
	undo, err := plan()
	if err != nil {
		return nil, p.fail(ctx, intent, action, err)
	}
	return p.settleRecord(ctx, intent, UpdateKind, undo, "Changed the event on the "+string(target)+" calendar")
}

func (p *Proposals) ExecuteRSVP(ctx context.Context, prop *proposals.Proposal, _ any) (any, error) {
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
	target, err := ParseTarget(info.Calendar)
	if err != nil {
		return nil, err
	}
	if err := p.guard(ctx, target); err != nil {
		return nil, err
	}
	var plan func() (undoPlan, error)
	switch target {
	case ICloud:
		plan, err = planICloudRSVP(ctx, p.env(), set, info.UID, info.Response)
	case Google:
		plan, err = planGoogleRSVP(ctx, p.env(), set, info.UID, info.Response)
	}
	if err != nil {
		return nil, err
	}
	action := actionPayload{Type: RSVPKind, Calendar: string(target), UID: info.UID, Response: info.Response}
	intent, err := p.begin(ctx, prop, action)
	if err != nil {
		return nil, err
	}
	undo, err := plan()
	if err != nil {
		return nil, p.fail(ctx, intent, action, err)
	}
	return p.settleRecord(ctx, intent, RSVPKind, undo, "Replied "+info.Response)
}

func (p *Proposals) Restore(ctx context.Context, plan json.RawMessage) (any, error) {
	var info undoPlan
	if err := json.Unmarshal(plan, &info); err != nil {
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
	if err := p.guard(ctx, target); err != nil {
		return nil, err
	}
	switch target {
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
	}
	return RestoreResult{Restored: info.UID}, nil
}
