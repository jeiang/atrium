package birthdays

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/timex"
	"golang.org/x/text/cases"
)

const (
	DefaultDays = 14
	MaxDays     = 366
)

func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func Limits(ctx context.Context, q db.Querier) (defaultDays, maxDays int, err error) {
	return mail.TuningBirthdaysDefaultDays, mail.TuningBirthdaysMaxDays, nil
}

func validDate(year, month, day int) (time.Time, bool) {
	if month < 1 || month > 12 || day < 1 {
		return time.Time{}, false
	}
	when := Date(year, time.Month(month), day)
	return when, int(when.Month()) == month && when.Day() == day
}

func Occurrence(month, day int, today time.Time) (time.Time, error) {
	for _, year := range [...]int{today.Year(), today.Year() + 1} {
		when, ok := validDate(year, month, day)
		if !ok {
			when = Date(year, time.February, 28)
		}
		if !when.Before(today) {
			return when, nil
		}
	}
	return time.Time{}, errors.New("birthday date is unreachable")
}

type Dismissals map[string]string

func DismissedKeys(ctx context.Context, q db.Querier) (Dismissals, error) {
	rows, err := q.QueryContext(ctx, "SELECT key, occurrence FROM watch_dismissed")
	if err != nil {
		return nil, err
	}
	out := Dismissals{}
	_, err = db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var key, occurrence string
		err := r.Scan(&key, &occurrence)
		out[key] = occurrence
		return struct{}{}, err
	})
	return out, err
}

func ItemKey(name string, entry contacts.Date) string {
	return fmt.Sprintf("%s|%s|%02d-%02d", cases.Fold().String(name), entry.Kind, entry.Month, entry.Day)
}

type Item struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Label     string   `json:"label"`
	Date      string   `json:"date"`
	DaysUntil int      `json:"days_until"`
	Turning   *int     `json:"turning"`
	Emails    []string `json:"emails"`
	Key       string   `json:"key"`
}

func Upcoming(cards []contacts.Card, today time.Time, days int, dismissed Dismissals) ([]Item, error) {
	today = Date(today.Year(), today.Month(), today.Day())
	out := []Item{}
	for _, card := range cards {
		for _, entry := range card.Dates {
			when, err := Occurrence(entry.Month, entry.Day, today)
			if err != nil {
				return nil, err
			}
			away := int(when.Sub(today) / (24 * time.Hour))
			if away > days {
				continue
			}
			key := ItemKey(card.Name, entry)
			iso := when.Format("2006-01-02")
			if dismissed[key] == iso {
				continue
			}
			var turning *int
			if entry.Year != nil && *entry.Year != 0 {
				turning = new(when.Year() - *entry.Year)
			}
			emails := card.Emails
			if emails == nil {
				emails = []string{}
			}
			out = append(out, Item{Name: card.Name, Kind: entry.Kind, Label: entry.Label, Date: iso, DaysUntil: away,
				Turning: turning, Emails: emails, Key: key})
		}
	}
	slices.SortStableFunc(out, func(a, b Item) int {
		if a.DaysUntil != b.DaysUntil {
			return a.DaysUntil - b.DaysUntil
		}
		if c := strings.Compare(cases.Fold().String(a.Name), cases.Fold().String(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return out, nil
}

func UpcomingFor(ctx context.Context, q db.Querier, today time.Time, days int) (items []Item, ok bool, err error) {
	dir, err := contacts.Dir(ctx, q)
	if err != nil {
		return nil, false, err
	}
	if dir == "" {
		return nil, false, nil
	}
	dismissed, err := DismissedKeys(ctx, q)
	if err != nil {
		return nil, false, err
	}
	items, err = Upcoming(contacts.Load(dir), today, days, dismissed)
	return items, err == nil, err
}

func Dismiss(ctx context.Context, q db.Querier, key, occurrenceDate string, ts float64) error {
	if _, ok := timex.ParseDay(occurrenceDate); !ok {
		return fmt.Errorf("%w: Invalid isoformat string: %s", httpx.ErrPayload, strconv.Quote(occurrenceDate))
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO watch_dismissed (key, occurrence, ts) VALUES (?, ?, ?) "+
			"ON CONFLICT(key) DO UPDATE SET occurrence = excluded.occurrence, ts = excluded.ts",
		key, occurrenceDate, ts)
	return err
}
