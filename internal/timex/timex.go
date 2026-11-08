package timex

import (
	"math"
	"math/big"
	"slices"
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

func SplitTimestamp(ts float64) (sec, us int64) {
	whole := math.Floor(ts)
	frac := math.RoundToEven((ts - whole) * microsecondsPerSec)
	if frac >= microsecondsPerSec {
		whole++
		frac -= microsecondsPerSec
	}
	return int64(whole), int64(frac)
}
