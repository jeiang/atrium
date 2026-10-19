package calendar

import (
	"time"

	"github.com/emersion/go-ical"
	"github.com/jeiang/atrium/internal/py"
)

// Invite is one upcoming event of an invitation, as event_candidates.invites_in builds it. End is nil without a
// DTEND. Attendees are never read.
type Invite struct {
	UID      string
	Title    string
	Start    string
	End      *string
	AllDay   bool
	Location string
	Notes    string
}

// Raw is the invite as the proposal input of calendar_proposals.propose, aimed at target.
func (i Invite) Raw(target string) py.Dict {
	var end any
	if i.End != nil {
		end = *i.End
	}
	return py.Dict{
		{Key: "uid", Value: i.UID}, {Key: "title", Value: i.Title}, {Key: "start", Value: i.Start}, {Key: "end", Value: end},
		{Key: "all_day", Value: i.AllDay}, {Key: "location", Value: i.Location}, {Key: "notes", Value: i.Notes},
		{Key: "calendar", Value: target},
	}
}

var skippedMethods = map[string]bool{"CANCEL": true, "REPLY": true, "COUNTER": true, "DECLINECOUNTER": true, "REFRESH": true}

// InvitesIn is the part of invites_in for one text/calendar payload: its VEVENTs that are not cancelled and not
// over at now, one per UID. methodParam is the part's Content-Type method parameter, used when the calendar has
// no METHOD. A payload icalendar cannot parse has none.
func InvitesIn(data []byte, methodParam string, now time.Time) []Invite {
	cal, err := decodeICS(data)
	if err != nil {
		return nil
	}
	method := ""
	if p := cal.Props.Get(ical.PropMethod); p != nil {
		method = unescapeText(p.Value)
	}
	if method == "" {
		method = methodParam
	}
	if skippedMethods[py.Upper(method)] {
		return nil
	}
	events, err := parseEvents(cal)
	if err != nil {
		return nil
	}
	cutoff := now.UTC()
	var out []Invite
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.dtstart == nil || py.Upper(ev.status) == "CANCELLED" {
			continue
		}
		start := *ev.dtstart
		last := start
		if ev.dtend != nil {
			last = *ev.dtend
		}
		if over(last, cutoff) {
			continue
		}
		uid := ev.uidKey()
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		title := py.Strip(ev.summary)
		if title == "" {
			title = "Event"
		}
		invite := Invite{
			UID: uid, Title: title, Start: start.iso(), AllDay: start.isDate(),
			Location: py.Strip(ev.location), Notes: py.Clip(py.Strip(ev.description), 1000),
		}
		if ev.dtend != nil {
			invite.End = new(ev.dtend.iso())
		}
		out = append(out, invite)
	}
	return out
}

func over(last Value, cutoff time.Time) bool {
	switch last.kind {
	case kindAware:
		return last.t.Before(cutoff)
	case kindDate:
		y, m, d := cutoff.Date()
		return last.t.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	}
	return false
}

func parseEvents(cal *ical.Calendar) ([]*icsEvent, error) {
	var events []*icsEvent
	err := walkEvents(cal.Component, &zones{custom: map[string]*time.Location{}}, &events)
	return events, err
}
