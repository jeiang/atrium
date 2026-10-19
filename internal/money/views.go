package money

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

func TrackedSince(ctx context.Context, q db.Querier) (string, error) {
	var value sql.NullString
	err := q.QueryRowContext(ctx, "SELECT value FROM settings WHERE key = ?", SinceKey).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value.String, err
}

func SetTrackedSince(ctx context.Context, q db.Querier, value string) (string, error) {
	value = py.Strip(value)
	if value != "" {
		parsed, ok := ParseDate(value)
		if !ok {
			return "", LedgerError("tracking start must be a YYYY-MM-DD date")
		}
		value = isoDate(parsed)
	}
	_, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)", SinceKey, value)
	return value, err
}

type Filter struct {
	Start     string
	End       string
	AccountID int64
	Payee     string
	Category  string
	Kind      string
	Currency  string
}

type ListedTransaction struct {
	TransactionRow
	Payee        *string `json:"payee"`
	Account      *string `json:"account"`
	Category     *string `json:"category"`
	SourceCount  int64   `json:"source_count"`
	MessageID    *int64  `json:"message_id"`
	DisplayPayee string  `json:"display_payee"`
	IsTransfer   bool    `json:"is_transfer"`
}

const listSQL = "SELECT " + transactionColumns + ", p.name, a.name, COALESCE(c.name, pc.name), " +
	"(SELECT COUNT(*) FROM transaction_sources s WHERE s.transaction_id = t.id), " +
	"(SELECT MIN(s.message_id) FROM transaction_sources s WHERE s.transaction_id = t.id) " +
	"FROM transactions t LEFT JOIN payees p ON p.id = t.payee_id LEFT JOIN money_accounts a ON a.id = t.account_id " +
	"LEFT JOIN categories c ON c.id = t.category_id LEFT JOIN categories pc ON pc.id = p.category_id"

func ListTransactions(ctx context.Context, q db.Querier, f Filter, unassigned bool, limit int, dismissed bool, today time.Time) ([]ListedTransaction, error) {
	where := []string{"t.dismissed = ''"}
	if dismissed {
		where = []string{"t.dismissed != ''"}
	}
	var params []any
	if f.Start != "" {
		where, params = append(where, "t.date >= ?"), append(params, f.Start)
	}
	if f.End != "" {
		where, params = append(where, "t.date <= ?"), append(params, f.End)
	}
	if f.AccountID != 0 {
		where, params = append(where, "t.account_id = ?"), append(params, f.AccountID)
	}
	if f.Payee != "" {
		like := "%" + f.Payee + "%"
		where, params = append(where, "(p.name LIKE ? OR t.payee_raw LIKE ?)"), append(params, like, like)
	}
	if f.Category != "" {
		where, params = append(where, "COALESCE(c.name, pc.name) = ?"), append(params, f.Category)
	}
	if f.Kind != "" {
		where, params = append(where, "t.kind = ?"), append(params, f.Kind)
	}
	if f.Currency != "" {
		where, params = append(where, "(t.currency = ? OR (t.currency IS NULL AND t.original_currency = ?))"), append(params, f.Currency, f.Currency)
	}
	if unassigned {
		where = append(where, "(t.account_id IS NULL OR t.review != '')")
	}
	since, err := TrackedSince(ctx, q)
	if err != nil {
		return nil, err
	}
	if since != "" {
		billed, err := Bills(ctx, q, today, nil)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(billed))
		for _, b := range billed {
			ids = append(ids, b.PayeeID)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if len(ids) > 0 {
			where = append(where, fmt.Sprintf("(t.date >= ? OR t.payee_id IN (%s))", placeholders(len(ids))))
		} else {
			where = append(where, "t.date >= ?")
		}
		params = append(params, since)
		for _, id := range ids {
			params = append(params, id)
		}
	}
	rows, err := q.QueryContext(ctx, listSQL+" WHERE "+strings.Join(where, " AND ")+" ORDER BY t.date DESC, t.id DESC LIMIT ?", append(params, limit)...)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (ListedTransaction, error) {
		var t ListedTransaction
		if err := r.Scan(append(t.targets(), &t.Payee, &t.Account, &t.Category, &t.SourceCount, &t.MessageID)...); err != nil {
			return t, err
		}
		t.DisplayPayee = displayPayee(t.Payee, t.PayeeRaw, t.Kind)
		t.IsTransfer = contains(TransferKinds, t.Kind)
		return t, nil
	})
}

func Unassigned(ctx context.Context, q db.Querier, limit int, today time.Time) ([]ListedTransaction, error) {
	return ListTransactions(ctx, q, Filter{}, true, limit, false, today)
}

func PeriodBounds(period string, today time.Time) (start, end string, err error) {
	if period == "" {
		period = "month"
	}
	period = py.Lower(py.Strip(period))
	if period == "month" {
		period = today.Format("2006-01")
	}
	bad := LedgerError("period must be YYYY-MM, YYYY, YYYY-MM-DD/YYYY-MM-DD, Nd or month")
	if m, err := periodMonthRE.FullMatch(period); err != nil {
		return "", "", err
	} else if m != nil {
		first, ok := ParseDate(period + "-01")
		if !ok || first.Year() == 9999 && first.Month() == time.December {
			return "", "", bad
		}
		return isoDate(first), isoDate(addDays(first.AddDate(0, 1, 0), -1)), nil
	}
	if m, err := periodYearRE.FullMatch(period); err != nil {
		return "", "", err
	} else if m != nil {
		return period + "-01-01", period + "-12-31", nil
	}
	if m, err := periodDaysRE.FullMatch(period); err != nil {
		return "", "", err
	} else if m != nil {
		days, err := py.IntString(period[:len(period)-1])
		if err != nil {
			return "", "", err
		}
		if days.Cmp(big.NewInt(999999999)) > 0 {
			return "", "", &py.Exception{Class: "OverflowError", Msg: fmt.Sprintf("days=%s; must have magnitude <= 999999999", days)}
		}
		from := addDays(today, -int(days.Int64()))
		if from.Year() < 1 {
			return "", "", &py.Exception{Class: "OverflowError", Msg: "date value out of range"}
		}
		return isoDate(from), isoDate(today), nil
	}
	if m, err := periodRangeRE.FullMatch(period); err != nil {
		return "", "", err
	} else if m != nil {
		from, okFrom := ParseDate(m.Group(1))
		to, okTo := ParseDate(m.Group(2))
		if okFrom && okTo && !from.After(to) {
			return m.Group(1), m.Group(2), nil
		}
	}
	return "", "", bad
}

type SpendGroup struct {
	Name  string  `json:"name"`
	Total float64 `json:"total"`
	Count int     `json:"count"`
}

type SpendBucket struct {
	Total  float64      `json:"total"`
	Groups []SpendGroup `json:"groups"`
}

func Spending(ctx context.Context, q db.Querier, start, end, groupBy, currency string, today time.Time) (map[string]SpendBucket, error) {
	if groupBy != "category" && groupBy != "payee" && groupBy != "account" {
		return nil, LedgerError("group_by must be category, payee or account")
	}
	rows, err := ListTransactions(ctx, q, Filter{Start: start, End: end, Currency: currency}, false, AllRows, false, today)
	if err != nil {
		return nil, err
	}
	type bucket struct {
		total  float64
		order  []string
		groups map[string]*SpendGroup
	}
	buckets := map[string]*bucket{}
	for _, r := range rows {
		if !contains(SpendingKinds, r.Kind) || r.Amount == nil {
			continue
		}
		value := *r.Amount
		if r.Kind == Refund {
			value = -value
		}
		var name string
		switch groupBy {
		case "category":
			name = Other
			if r.Category != nil && *r.Category != "" {
				name = *r.Category
			}
		case "payee":
			name = r.DisplayPayee
		default:
			name = "unassigned"
			if r.Account != nil && *r.Account != "" {
				name = *r.Account
			}
		}
		code := ""
		if r.Currency != nil {
			code = *r.Currency
		}
		b := buckets[code]
		if b == nil {
			b = &bucket{groups: map[string]*SpendGroup{}}
			buckets[code] = b
		}
		b.total += value
		g := b.groups[name]
		if g == nil {
			g = &SpendGroup{Name: name}
			b.groups[name] = g
			b.order = append(b.order, name)
		}
		g.Total += value
		g.Count++
	}
	out := make(map[string]SpendBucket, len(buckets))
	for code, b := range buckets {
		groups := make([]SpendGroup, 0, len(b.order))
		for _, name := range b.order {
			g := *b.groups[name]
			g.Total = round2(g.Total)
			groups = append(groups, g)
		}
		slices.SortStableFunc(groups, func(a, c SpendGroup) int {
			switch {
			case -a.Total < -c.Total:
				return -1
			case -a.Total > -c.Total:
				return 1
			}
			return 0
		})
		out[code] = SpendBucket{Total: round2(b.total), Groups: groups}
	}
	return out, nil
}

type Alias struct {
	ID      int64  `json:"id"`
	PayeeID int64  `json:"payee_id"`
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
}

type PayeeListing struct {
	Payee
	Transactions int64   `json:"transactions"`
	LastDate     *string `json:"last_date"`
	Aliases      []Alias `json:"aliases"`
}

func ListPayees(ctx context.Context, q db.Querier, query string, limit int) ([]PayeeListing, error) {
	where, args := "", []any{}
	if query != "" {
		where, args = " WHERE p.name LIKE ?", append(args, "%"+query+"%")
	}
	rows, err := q.QueryContext(ctx,
		"SELECT "+payeeColumns+", (SELECT COUNT(*) FROM transactions t WHERE t.payee_id = p.id), "+
			"(SELECT MAX(t.date) FROM transactions t WHERE t.payee_id = p.id) "+
			"FROM payees p LEFT JOIN categories c ON c.id = p.category_id"+where+" ORDER BY p.name LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (PayeeListing, error) {
		var l PayeeListing
		var err error
		l.Payee, err = scanPayee(r, &l.Transactions, &l.LastDate)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	aliasRows, err := q.QueryContext(ctx, "SELECT id, payee_id, pattern, kind FROM payee_aliases ORDER BY kind, pattern")
	if err != nil {
		return nil, err
	}
	aliases, err := db.Collect(aliasRows, func(r *sql.Rows) (Alias, error) {
		var a Alias
		return a, r.Scan(&a.ID, &a.PayeeID, &a.Pattern, &a.Kind)
	})
	if err != nil {
		return nil, err
	}
	byPayee := map[int64][]Alias{}
	for _, a := range aliases {
		byPayee[a.PayeeID] = append(byPayee[a.PayeeID], a)
	}
	for i := range found {
		found[i].Aliases = byPayee[found[i].ID]
		if found[i].Aliases == nil {
			found[i].Aliases = []Alias{}
		}
	}
	return found, nil
}
