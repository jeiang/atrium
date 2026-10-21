package birthdays

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/contacts"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/py"
)

const (
	DefaultDays = 14
	MaxDays     = 366
	DismissKind = "birthday"
)

func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func Limits(ctx context.Context, q db.Querier) (defaultDays, maxDays int, err error) {
	r := mail.Read(ctx, q)
	defaultDays = r.Int(mail.KeyBirthdaysDefaultDays)
	maxDays = r.Int(mail.KeyBirthdaysMaxDays)
	return defaultDays, maxDays, r.Err()
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
	return time.Time{}, errors.New("AssertionError: unreachable")
}

type Dismissals map[[2]string]string

func DismissedKeys(ctx context.Context, q db.Querier) (Dismissals, error) {
	rows, err := q.QueryContext(ctx, "SELECT kind, key, occurrence FROM watch_dismissed")
	if err != nil {
		return nil, err
	}
	out := Dismissals{}
	_, err = db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var kind, key, occurrence string
		err := r.Scan(&kind, &key, &occurrence)
		out[[2]string{kind, key}] = occurrence
		return struct{}{}, err
	})
	return out, err
}

func ItemKey(name string, entry contacts.Date) string {
	return fmt.Sprintf("%s|%s|%02d-%02d", py.Casefold(name), entry.Kind, entry.Month, entry.Day)
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
			if dismissed[[2]string{DismissKind, key}] == iso {
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
		if c := strings.Compare(py.Casefold(a.Name), py.Casefold(b.Name)); c != 0 {
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
	if _, _, _, ok := py.DateFromISOFormat(occurrenceDate); !ok {
		return fmt.Errorf("%w: Invalid isoformat string: %s", httpx.ErrValue, py.Repr(occurrenceDate))
	}
	_, err := q.ExecContext(ctx,
		"INSERT INTO watch_dismissed (kind, key, occurrence, ts) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT(kind, key) DO UPDATE SET occurrence = excluded.occurrence, ts = excluded.ts",
		DismissKind, key, occurrenceDate, ts)
	return err
}
