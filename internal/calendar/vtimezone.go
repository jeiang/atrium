package calendar

import (
	"bytes"
	"encoding/binary"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

var zoneCache sync.Map

func zoneNamed(name string) *time.Location {
	if name == "" || name == "Local" {
		return nil
	}
	if cached, ok := zoneCache.Load(name); ok {
		loc, _ := cached.(*time.Location)
		return loc
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc = nil
	}
	zoneCache.Store(name, loc)
	return loc
}

type onset struct {
	at       int64
	offset   int
	dst      bool
	abbrev   string
	fromWall int64
}

func parseOffset(s string) (int, bool) {
	if len(s) < 5 || (s[0] != '+' && s[0] != '-') {
		return 0, false
	}
	h, ok1 := digitsAt(s, 1, 3)
	m, ok2 := digitsAt(s, 3, 5)
	sec := 0
	ok3 := true
	if len(s) >= 7 {
		sec, ok3 = digitsAt(s, 5, 7)
	}
	if !ok1 || !ok2 || !ok3 {
		return 0, false
	}
	total := h*3600 + m*60 + sec
	if s[0] == '-' {
		total = -total
	}
	return total, true
}

func registerZone(comp *ical.Component, z *zones) {
	tzid := comp.Props.Get(ical.PropTimezoneID)
	if tzid == nil {
		return
	}
	clean := strings.Trim(tzid.Value, "/")
	if zoneNamed(clean) != nil || zoneNamed(tzid.Value) != nil {
		return
	}
	if _, ok := z.custom[clean]; ok {
		return
	}
	if loc := buildZone(comp, clean); loc != nil {
		z.custom[clean] = loc
	}
}

func buildZone(comp *ical.Component, name string) *time.Location {
	var onsets []onset
	for _, child := range comp.Children {
		if child.Name != ical.CompTimezoneStandard && child.Name != ical.CompTimezoneDaylight {
			continue
		}
		onsets = append(onsets, componentOnsets(child)...)
	}
	if len(onsets) == 0 {
		return nil
	}
	slices.SortStableFunc(onsets, func(a, b onset) int { return int(a.at - b.at) })
	first := onsets[0]
	for _, o := range onsets {
		if !o.dst {
			first = o
			break
		}
	}
	data := tzif(first, onsets)
	loc, err := time.LoadLocationFromTZData(name, data)
	if err != nil {
		return nil
	}
	return loc
}

func componentOnsets(comp *ical.Component) []onset {
	dtstart := comp.Props.Get(ical.PropDateTimeStart)
	from := comp.Props.Get(ical.PropTimezoneOffsetFrom)
	to := comp.Props.Get(ical.PropTimezoneOffsetTo)
	if dtstart == nil || from == nil || to == nil {
		return nil
	}
	fromOff, ok1 := parseOffset(from.Value)
	toOff, ok2 := parseOffset(to.Value)
	start, ok3 := clockValue(dtstart.Value, nil)
	if !ok1 || !ok2 || !ok3 || start.isAware() {
		return nil
	}
	abbrev := ""
	if p := comp.Props.Get(ical.PropTimezoneName); p != nil {
		abbrev = unescapeText(p.Value)
	}
	dst := comp.Name == ical.CompTimezoneDaylight
	var walls []time.Time
	walls = append(walls, start.t)
	for _, p := range comp.Props.Values(ical.PropRecurrenceDates) {
		for _, item := range strings.Split(p.Value, ",") {
			if v, ok := clockValue(item, nil); ok && !v.isAware() {
				walls = append(walls, v.t)
			}
		}
	}
	for _, p := range comp.Props.Values(ical.PropRecurrenceRule) {
		rule, err := parseRule(p.Value)
		if err != nil {
			continue
		}
		rule.opt.Dtstart = start.t
		if rule.hasUntil && !rule.untilUTC {
			rule.opt.Until = rule.until
		} else if rule.hasUntil {
			rule.opt.Until = wall(rule.until.Add(time.Duration(fromOff) * time.Second))
		} else {
			rule.opt.Until = time.Date(2100, 12, 31, 0, 0, 0, 0, time.UTC)
		}
		r, err := rrule.NewRRule(rule.opt)
		if err != nil {
			continue
		}
		walls = append(walls, r.All()...)
	}
	out := make([]onset, 0, len(walls))
	for _, w := range walls {
		out = append(out, onset{at: w.Unix() - int64(fromOff), offset: toOff, dst: dst, abbrev: abbrev})
	}
	return out
}

func tzif(first onset, onsets []onset) []byte {
	type zoneType struct {
		offset int
		dst    bool
		abbrev string
	}
	var types []zoneType
	typeOf := func(o onset) byte {
		t := zoneType{o.offset, o.dst, o.abbrev}
		i := slices.Index(types, t)
		if i < 0 {
			types = append(types, t)
			i = len(types) - 1
		}
		return byte(i)
	}
	times := []int64{-(1 << 59)}
	idx := []byte{typeOf(first)}
	for _, o := range onsets {
		times = append(times, o.at)
		idx = append(idx, typeOf(o))
	}
	var chars bytes.Buffer
	abbrevAt := make([]byte, len(types))
	for i, t := range types {
		abbrevAt[i] = byte(chars.Len())
		chars.WriteString(t.abbrev)
		chars.WriteByte(0)
	}
	header := func(timecnt, typecnt, charcnt int) []byte {
		var b bytes.Buffer
		b.WriteString("TZif2")
		b.Write(make([]byte, 15))
		for _, n := range []int{0, 0, 0, timecnt, typecnt, charcnt} {
			_ = binary.Write(&b, binary.BigEndian, uint32(n))
		}
		return b.Bytes()
	}
	var out bytes.Buffer
	out.Write(header(0, 1, 1))
	out.Write([]byte{0, 0, 0, 0, 0, 0, 0})
	out.Write(header(len(times), len(types), chars.Len()))
	for _, t := range times {
		_ = binary.Write(&out, binary.BigEndian, t)
	}
	out.Write(idx)
	for i, t := range types {
		_ = binary.Write(&out, binary.BigEndian, int32(t.offset))
		dst := byte(0)
		if t.dst {
			dst = 1
		}
		out.Write([]byte{dst, abbrevAt[i]})
	}
	out.Write(chars.Bytes())
	out.WriteString("\n\n")
	return out.Bytes()
}
