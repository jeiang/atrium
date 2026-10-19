package money

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func round2(v float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(v+0.0, 'f', 2, 64), 64)
	if err != nil {
		return v
	}
	return rounded
}

func roundPtr(v *float64) *float64 {
	if v == nil {
		return nil
	}
	r := round2(*v)
	return &r
}

func clip(text string, limit int) string {
	text = strings.Join(py.Split(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

func lastDigits(text string, n int) (string, error) {
	digits, err := nonDigitRE.Sub("", text, 0)
	if err != nil {
		return "", err
	}
	runes := []rune(digits)
	if len(runes) > n {
		runes = runes[len(runes)-n:]
	}
	return string(runes), nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func joinList(list []string) string { return strings.Join(list, ", ") }

func civil(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func LocalDate(ts float64, loc *time.Location) time.Time {
	sec, _ := py.SplitTimestamp(ts)
	t := time.Unix(sec, 0).In(loc)
	return civil(t.Year(), t.Month(), t.Day())
}

func ParseDate(s string) (time.Time, bool) {
	y, m, d, ok := py.DateFromISOFormat(s)
	if !ok {
		return time.Time{}, false
	}
	return civil(y, time.Month(m), d), true
}

func isoDate(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

func daysBetween(a, b time.Time) int { return int(math.Round(b.Sub(a).Hours() / 24)) }

func addDays(t time.Time, n int) time.Time { return t.AddDate(0, 0, n) }

func queryIDs(ctx context.Context, q db.Querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (int64, error) {
		var id int64
		return id, r.Scan(&id)
	})
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
