package timex

import (
	"math"
	"math/big"
	"slices"
	"strings"
	"time"
)

type DateTime struct {
	Year, Month, Day                  int
	Hour, Minute, Second, Microsecond int
	Aware                             bool
	OffsetSeconds, OffsetMicroseconds int
}

const (
	secondsPerDay      = 86400
	microsecondsPerSec = 1_000_000
)

func (d DateTime) Timestamp(loc *time.Location) float64 {
	wall := time.Date(d.Year, time.Month(d.Month), d.Day, d.Hour, d.Minute, d.Second, 0, time.UTC).Unix()
	var unix int64
	if d.Aware {
		unix = wall - int64(d.OffsetSeconds)
	} else {
		unix = WallToUnix(loc, wall)
	}
	micros := new(big.Int).SetInt64(unix)
	micros.Mul(micros, big.NewInt(microsecondsPerSec))
	micros.Add(micros, big.NewInt(int64(d.Microsecond)-int64(d.OffsetMicroseconds)))
	f, _ := new(big.Rat).SetFrac(micros, big.NewInt(microsecondsPerSec)).Float64()
	return f
}

func offsetAt(loc *time.Location, unix int64) int64 {
	_, off := time.Unix(unix, 0).In(loc).Zone()
	return int64(off)
}

func WallToUnix(loc *time.Location, wall int64) int64 {
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

func ParseDay(value string) (time.Time, bool) {
	day, err := time.Parse("2006-01-02", value)
	return day, err == nil
}

func SplitTimestamp(ts float64) (sec, us int64) {
	whole := math.Floor(ts)
	frac := math.RoundToEven((ts - whole) * microsecondsPerSec)
	if frac >= microsecondsPerSec {
		whole++
		frac -= microsecondsPerSec
	}
	return int64(whole), int64(frac)
}

// ParseISO parses a date or datetime without a zone as UTC wall time.
func ParseISO(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ISOFormat renders t with the requested timespec.
func ISOFormat(t time.Time, timespec string) string {
	switch timespec {
	case "minutes":
		return t.Format("2006-01-02T15:04")
	case "seconds":
		return t.Format("2006-01-02T15:04:05")
	case "milliseconds":
		return t.Format("2006-01-02T15:04:05.000")
	}
	return t.Format("2006-01-02T15:04:05")
}

// TimestampIn interprets t's wall-clock fields in loc and returns Unix seconds.
func TimestampIn(t time.Time, loc *time.Location) float64 {
	wall := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC).Unix()
	return float64(WallToUnix(loc, wall)) + float64(t.Nanosecond())/1e9
}
