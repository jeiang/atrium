package weather

import (
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/homecard"
)

type Reading struct {
	State     homecard.State
	Forecast  *Forecast
	FetchedAt time.Time
	Failure   string
}

type Last struct {
	mu        sync.Mutex
	key       string
	forecast  *Forecast
	fetchedAt time.Time
	failure   string
	failedAt  time.Time
	running   bool
}

var lasts sync.Map

func LastFor(d *db.DB) *Last {
	found, _ := lasts.LoadOrStore(d, &Last{})
	return found.(*Last)
}

func (l *Last) Read(key string, now time.Time) (Reading, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if key != l.key {
		l.key, l.forecast, l.fetchedAt, l.failure, l.failedAt, l.running = key, nil, time.Time{}, "", time.Time{}, false
	}
	due := !l.running &&
		(l.forecast == nil || now.Sub(l.fetchedAt) >= TuningHomeWeatherRefreshMinutes*time.Minute) &&
		(l.failedAt.IsZero() || now.Sub(l.failedAt) >= TuningHomeWeatherRetrySeconds*time.Second)
	if due {
		l.running = true
	}
	out := Reading{Forecast: l.forecast, FetchedAt: l.fetchedAt}
	failedSince := !l.failedAt.IsZero() && l.failedAt.After(l.fetchedAt)
	if failedSince {
		out.Failure = l.failure
	}
	switch {
	case l.forecast == nil && failedSince && !due:
		out.State = homecard.StateFailed
	case l.forecast == nil:
		out.State = homecard.StateLoading
	case failedSince || now.Sub(l.fetchedAt) >= TuningHomeWeatherStaleMinutes*time.Minute:
		out.State = homecard.StateStale
	default:
		out.State = homecard.StateOK
	}
	return out, due
}

func (l *Last) Done(key string, forecast *Forecast, failure string, at time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if key != l.key {
		return false
	}
	l.running = false
	if forecast != nil {
		l.forecast, l.fetchedAt, l.failure, l.failedAt = forecast, at, "", time.Time{}
		return true
	}
	l.failure, l.failedAt = failure, at
	return true
}
