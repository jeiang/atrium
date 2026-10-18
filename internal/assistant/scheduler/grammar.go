package scheduler

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	MinIntervalSeconds  = 3600
	MaxActiveJobs       = 20
	checkedOccurrences  = 64
	searchDays          = 366 * 5
	KindDaily           = "daily"
	KindWeekly          = "weekly"
	KindCron            = "cron"
	KindOnce            = "once"
	errUnrecognised     = "unrecognised schedule; use an ISO datetime, 'daily HH:MM', 'weekly DOW HH:MM' or a 5-field cron expression"
	errNeverFires       = "that schedule never fires in the future"
	secondsPerDay       = 86400
	microsecondsPerSec  = 1_000_000
	dowBound            = 4
	cronFieldCount      = 5
	monthField          = 3
	maxParsedCronNumber = 1 << 30
)

type Error string

func (e Error) Error() string { return string(e) }

var timePattern = rx.MustCompile(`^(\d{1,2}):(\d{2})$`, 0)

var (
	days       = map[string]int{"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
	cronBounds = [cronFieldCount][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	cronNames  = map[int]map[string]int{
		monthField: {"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12},
		dowBound:   {"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6},
	}
	weekdays = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
)

type Spec struct {
	Kind    string
	TZ      string
	Loc     *time.Location
	OnceAt  float64
	Minutes []int
	Hours   []int
	DOM     []int
	Months  []int
	DOW     []int
	times   [][2]int
}

func (s Spec) Recurring() bool { return s.Kind != KindOnce }

func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

func pyLower(s string) string { return strings.ToLower(strings.ReplaceAll(s, "\u0130", "i\u0307")) }

func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Nd, r) {
			return false
		}
	}
	return true
}

func decimalValue(r rune) int {
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10
		}
	}
	return 0
}

func pyInt(s string) int {
	n := 0
	for _, r := range s {
		n = min(n*10+decimalValue(r), maxParsedCronNumber)
	}
	return n
}

func zone(name string) (*time.Location, error) {
	loc, err := config.Zone(name)
	if err != nil {
		return nil, Error(err.Error())
	}
	return loc, nil
}

func clock(text string) (int, int, error) {
	m, err := timePattern.Match(text)
	if err == nil && m != nil {
		h, mi := pyInt(m.Group(1)), pyInt(m.Group(2))
		if h <= 23 && mi <= 59 {
			return h, mi, nil
		}
	}
	return 0, 0, Error(fmt.Sprintf("bad time %s; use HH:MM (24-hour)", config.PyRepr(text)))
}

func cronValue(token string, index int) (int, error) {
	if v, ok := cronNames[index][pyLower(token)]; ok {
		return v, nil
	}
	if !isDecimal(token) {
		return 0, Error("bad cron value " + config.PyRepr(token))
	}
	return pyInt(token), nil
}

func cronField(text string, index int) ([]int, error) {
	low, high := cronBounds[index][0], cronBounds[index][1]
	var found [64]bool
	for part := range strings.SplitSeq(text, ",") {
		body, stepText, _ := strings.Cut(part, "/")
		if stepText != "" && !isDecimal(stepText) {
			return nil, Error("bad cron step " + config.PyRepr(part))
		}
		step := 1
		if stepText != "" {
			step = pyInt(stepText)
		}
		if step < 1 {
			return nil, Error("bad cron step " + config.PyRepr(part))
		}
		var start, end int
		var err error
		switch {
		case body == "*":
			start, end = low, high
			if index == dowBound {
				end = 6
			}
		case strings.Contains(body, "-"):
			a, b, _ := strings.Cut(body, "-")
			if start, err = cronValue(a, index); err != nil {
				return nil, err
			}
			if end, err = cronValue(b, index); err != nil {
				return nil, err
			}
		default:
			if start, err = cronValue(body, index); err != nil {
				return nil, err
			}
			end = start
			if stepText != "" {
				end = high
			}
		}
		if start < low || end > high || start > end {
			return nil, Error("cron value out of range in " + config.PyRepr(part))
		}
		for v := start; v <= end; v += step {
			if index == dowBound {
				found[v%7] = true
			} else {
				found[v] = true
			}
		}
	}
	var out []int
	for v, ok := range found {
		if ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func newSpec(kind, tz string, loc *time.Location, minutes, hours, dom, months, dow []int) Spec {
	s := Spec{Kind: kind, TZ: tz, Loc: loc, Minutes: minutes, Hours: hours, DOM: dom, Months: months, DOW: dow}
	for _, h := range hours {
		for _, m := range minutes {
			s.times = append(s.times, [2]int{h, m})
		}
	}
	return s
}

func Parse(text, tz string) (Spec, error) {
	text = strings.TrimFunc(text, pyIsSpace)
	if text == "" {
		return Spec{}, Error("schedule is empty")
	}
	loc, err := zone(tz)
	if err != nil {
		return Spec{}, err
	}
	words := strings.FieldsFunc(text, pyIsSpace)
	switch pyLower(words[0]) {
	case KindDaily:
		if len(words) != 2 {
			return Spec{}, Error("daily schedules look like 'daily 07:30'")
		}
		h, m, err := clock(words[1])
		if err != nil {
			return Spec{}, err
		}
		return newSpec(KindDaily, tz, loc, []int{m}, []int{h}, nil, nil, nil), nil
	case KindWeekly:
		var day int
		var ok bool
		if len(words) == 3 {
			day, ok = days[pyLower(firstRunes(words[1], 3))]
		}
		if !ok {
			return Spec{}, Error("weekly schedules look like 'weekly mon 07:30'")
		}
		h, m, err := clock(words[2])
		if err != nil {
			return Spec{}, err
		}
		return newSpec(KindWeekly, tz, loc, []int{m}, []int{h}, nil, nil, []int{(day + 1) % 7}), nil
	}
	if len(words) == cronFieldCount {
		var fields [cronFieldCount][]int
		for i, w := range words {
			if fields[i], err = cronField(w, i); err != nil {
				return Spec{}, err
			}
		}
		restricted := func(i int) []int {
			if words[i] == "*" {
				return nil
			}
			return fields[i]
		}
		return newSpec(KindCron, tz, loc, fields[0], fields[1], restricted(2), restricted(3), restricted(4)), nil
	}
	moment, ok := fromISOFormat(text)
	if !ok {
		return Spec{}, Error(errUnrecognised)
	}
	wall := civilSeconds(moment.year, moment.month, moment.day, moment.hour, moment.minute, moment.second)
	var unix int64
	if moment.aware {
		unix = wall - int64(moment.offsetSeconds)
	} else {
		unix = wallToUnix(loc, wall)
	}
	micros := new(big.Int).SetInt64(unix)
	micros.Mul(micros, big.NewInt(microsecondsPerSec))
	micros.Add(micros, big.NewInt(int64(moment.microsecond)-int64(moment.offsetMicroseconds)))
	onceAt, _ := new(big.Rat).SetFrac(micros, big.NewInt(microsecondsPerSec)).Float64()
	s := newSpec(KindOnce, tz, loc, []int{}, []int{}, nil, nil, nil)
	s.OnceAt = onceAt
	return s, nil
}

func firstRunes(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

func civilSeconds(y, mo, d, h, mi, s int) int64 {
	return time.Date(y, time.Month(mo), d, h, mi, s, 0, time.UTC).Unix()
}

func offsetAt(loc *time.Location, unix int64) int64 {
	_, off := time.Unix(unix, 0).In(loc).Zone()
	return int64(off)
}

// wallToUnix resolves a local wall time as zoneinfo does with fold=0: the earlier instant when it is
// ambiguous, the offset in effect before the transition when it falls in a gap.
func wallToUnix(loc *time.Location, wall int64) int64 {
	offsets := []int64{offsetAt(loc, wall-secondsPerDay), offsetAt(loc, wall), offsetAt(loc, wall+secondsPerDay)}
	best, found := int64(0), false
	for _, o := range offsets {
		if u := wall - o; offsetAt(loc, u) == o && (!found || u < best) {
			best, found = u, true
		}
	}
	if found {
		return best
	}
	return wall - offsetAt(loc, wall-slices.Max(offsets))
}

func pyTimestamp(ts float64) (int64, int64) {
	sec := math.Floor(ts)
	us := math.RoundToEven((ts - sec) * microsecondsPerSec)
	if us >= microsecondsPerSec {
		sec++
		us -= microsecondsPerSec
	}
	return int64(sec), int64(us)
}

func localTime(ts float64, loc *time.Location) time.Time {
	sec, us := pyTimestamp(ts)
	return time.Unix(sec, us*1000).In(loc)
}

func (s Spec) dayMatches(day time.Time) bool {
	if s.Months != nil && !slices.Contains(s.Months, int(day.Month())) {
		return false
	}
	domOK := s.DOM == nil || slices.Contains(s.DOM, day.Day())
	dowOK := s.DOW == nil || slices.Contains(s.DOW, int(day.Weekday()))
	if s.DOM != nil && s.DOW != nil {
		return domOK || dowOK
	}
	return domOK && dowOK
}

func (s Spec) NextAfter(after float64) (float64, bool) {
	if s.Kind == KindOnce {
		return s.OnceAt, s.OnceAt > after
	}
	start := localTime(after, s.Loc)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	for range searchDays {
		if s.dayMatches(day) {
			for _, t := range s.times {
				stamp := float64(wallToUnix(s.Loc, day.Unix()+int64(t[0])*3600+int64(t[1])*60))
				if stamp > after {
					return stamp, true
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return 0, false
}

func (s Spec) MinGap(after float64) (float64, bool) {
	if !s.Recurring() {
		return 0, false
	}
	gap, found := 0.0, false
	previous, ok := s.NextAfter(after)
	for range checkedOccurrences {
		if !ok {
			break
		}
		current, more := s.NextAfter(previous)
		if !more {
			break
		}
		if d := current - previous; !found || d < gap {
			gap, found = d, true
		}
		previous = current
	}
	return gap, found
}

func Describe(text, tz string) string {
	s, err := Parse(text, tz)
	if err != nil {
		return text
	}
	if s.Kind == KindOnce {
		return localTime(s.OnceAt, s.Loc).Format("Once on Mon 02 Jan 2006 at 15:04")
	}
	if len(s.Hours) != 1 || len(s.Minutes) != 1 || s.Months != nil {
		return "Cron " + text
	}
	at := fmt.Sprintf("%02d:%02d", s.Hours[0], s.Minutes[0])
	switch {
	case s.DOM == nil && (s.DOW == nil || len(s.DOW) == 7):
		return "Every day at " + at
	case s.DOM == nil:
		ordered := slices.Clone(s.DOW)
		slices.SortFunc(ordered, func(a, b int) int { return (a+6)%7 - (b+6)%7 })
		names := make([]string, len(ordered))
		for i, d := range ordered {
			names[i] = weekdays[d]
			if len(ordered) > 1 {
				names[i] = names[i][:3]
			}
		}
		return "Every " + strings.Join(names, ", ") + " at " + at
	case s.DOW == nil:
		parts := make([]string, len(s.DOM))
		for i, d := range s.DOM {
			parts[i] = strconv.Itoa(d)
		}
		return "Day " + strings.Join(parts, ", ") + " of every month at " + at
	}
	return "Cron " + text
}

func Validate(text, tz string, now float64, enforceMin bool) (Spec, float64, error) {
	s, err := Parse(text, tz)
	if err != nil {
		return Spec{}, 0, err
	}
	due, ok := s.NextAfter(now)
	if !ok {
		return Spec{}, 0, Error(errNeverFires)
	}
	if enforceMin && s.Recurring() {
		if gap, ok := s.MinGap(now); ok && gap < MinIntervalSeconds {
			return Spec{}, 0, Error(fmt.Sprintf("recurring schedules must be at least %d hour apart", MinIntervalSeconds/3600))
		}
	}
	return s, due, nil
}
