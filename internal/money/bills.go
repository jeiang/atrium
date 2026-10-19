package money

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
)

type Bill struct {
	PayeeID          int64              `json:"payee_id"`
	Payee            string             `json:"payee"`
	Category         *string            `json:"category"`
	Cadence          string             `json:"cadence"`
	Typical          map[string]float64 `json:"typical"`
	Currencies       []string           `json:"-"`
	LastCharge       string             `json:"last_charge"`
	NextExpected     *string            `json:"next_expected"`
	DaysUntil        *int               `json:"days_until"`
	Override         *string            `json:"override"`
	SubscriptionSeen bool               `json:"subscription_seen"`
	Charges          int                `json:"charges"`
}

func inferCadence(days []time.Time) string {
	if len(days) < 2 {
		return ""
	}
	gaps := make([]int, 0, len(days)-1)
	for i := 1; i < len(days); i++ {
		gaps = append(gaps, daysBetween(days[i-1], days[i]))
	}
	for _, name := range cadenceOrder {
		length := cadenceDays[name]
		if !slices.ContainsFunc(gaps, func(g int) bool { return abs(g-length) > cadenceSlack[name] }) {
			return name
		}
	}
	return ""
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func median(values []float64) float64 {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

func similar(amounts []float64) bool {
	middle := median(amounts)
	if !(middle > 0) {
		return false
	}
	for _, a := range amounts {
		if math.Abs(a-middle) > billAmountTolerance*middle {
			return false
		}
	}
	return true
}

type charge struct {
	date   time.Time
	amount float64
}

func Bills(ctx context.Context, q db.Querier, today time.Time, upcomingDays *int) ([]Bill, error) {
	payees, err := ListPayees(ctx, q, "", AllRows)
	if err != nil {
		return nil, err
	}
	out := []Bill{}
	for _, p := range payees {
		if p.RecurringOverride != nil && *p.RecurringOverride == NotBill {
			continue
		}
		rows, err := q.QueryContext(ctx,
			"SELECT date, COALESCE(amount, original_amount), COALESCE(currency, original_currency) FROM transactions "+
				"WHERE payee_id = ? AND kind = ? AND dismissed = '' ORDER BY date", p.ID, Purchase)
		if err != nil {
			return nil, err
		}
		type row struct {
			date     string
			amount   float64
			currency string
		}
		found, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
			var x row
			return x, r.Scan(&x.date, &x.amount, &x.currency)
		})
		if err != nil {
			return nil, err
		}
		var currencies []string
		series := map[string][]charge{}
		for _, r := range found {
			day, ok := ParseDate(r.date)
			if !ok {
				return nil, LedgerError("invalid isoformat string: '" + r.date + "'")
			}
			if _, seen := series[r.currency]; !seen {
				currencies = append(currencies, r.currency)
			}
			series[r.currency] = append(series[r.currency], charge{day, r.amount})
		}
		cadence := ""
		typical := map[string]float64{}
		var last time.Time
		for _, currency := range currencies {
			items := series[currency]
			var dates []time.Time
			amounts := make([]float64, 0, len(items))
			for _, c := range items {
				dates = append(dates, c.date)
				amounts = append(amounts, c.amount)
			}
			slices.SortFunc(dates, func(a, b time.Time) int { return a.Compare(b) })
			dates = slices.CompactFunc(dates, func(a, b time.Time) bool { return a.Equal(b) })
			typical[currency] = round2(median(amounts))
			found := ""
			if len(dates) >= 2 && similar(amounts) {
				found = inferCadence(dates)
			}
			if cadence == "" {
				cadence = found
			}
			if last.IsZero() || dates[len(dates)-1].After(last) {
				last = dates[len(dates)-1]
			}
		}
		forced := p.RecurringOverride != nil && *p.RecurringOverride == ForceBill
		if (!forced && p.SubscriptionSeen == 0 && cadence == "") || last.IsZero() {
			continue
		}
		if cadence == "" {
			cadence = Unknown
			if _, known := cadenceDays[p.Cadence]; known {
				cadence = p.Cadence
			}
		}
		var next *time.Time
		var nextISO *string
		var daysUntil *int
		if length, known := cadenceDays[cadence]; known {
			n := addDays(last, length)
			next = &n
			iso := isoDate(n)
			nextISO = &iso
			d := daysBetween(today, n)
			daysUntil = &d
		}
		if upcomingDays != nil && (next == nil || next.Before(today) || next.After(addDays(today, *upcomingDays))) {
			continue
		}
		out = append(out, Bill{
			PayeeID: p.ID, Payee: p.Name, Category: p.Category, Cadence: cadence, Typical: typical, Currencies: currencies,
			LastCharge: isoDate(last), NextExpected: nextISO, DaysUntil: daysUntil, Override: p.RecurringOverride,
			SubscriptionSeen: p.SubscriptionSeen != 0, Charges: len(found),
		})
	}
	slices.SortStableFunc(out, func(a, b Bill) int {
		if (a.NextExpected == nil) != (b.NextExpected == nil) {
			if a.NextExpected == nil {
				return 1
			}
			return -1
		}
		if a.NextExpected != nil {
			if c := strings.Compare(*a.NextExpected, *b.NextExpected); c != 0 {
				return c
			}
		}
		return strings.Compare(a.Payee, b.Payee)
	})
	return out, nil
}
