package money

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/pyhtml"
)

type rateTables struct {
	tables [][][]string
	dates  []string
}

func parseRateTables(markup string) (rateTables, error) {
	var (
		out         rateTables
		inTable     bool
		inDate      bool
		rows        [][]string
		row         []string
		rowOpen     bool
		cell        string
		cellOpen    bool
		date        string
		pendingDate string
		failure     error
	)
	collapse := func(s string) string { return strings.Join(py.Split(s), " ") }
	parser := &pyhtml.Parser{
		StartTag: func(tag string, attrs []pyhtml.Attr) {
			if failure != nil {
				return
			}
			var class *string
			found := false
			for _, a := range attrs {
				if a.Name == "class" {
					class, found = a.Value, true
				}
			}
			var classes []string
			if found {
				if class == nil {
					failure = &py.Exception{Class: "AttributeError", Msg: "'NoneType' object has no attribute 'split'"}
					return
				}
				classes = py.Split(*class)
			}
			switch {
			case tag == "div" && slices.Contains(classes, "table_date"):
				inDate, date = true, ""
			case tag == "table" && slices.Contains(classes, "rate_table"):
				inTable, rows = true, [][]string{}
			case inTable && tag == "tr":
				row, rowOpen = []string{}, true
			case inTable && (tag == "td" || tag == "th"):
				cell, cellOpen = "", true
			}
		},
		EndTag: func(tag string) {
			switch {
			case tag == "div" && inDate:
				inDate, pendingDate = false, collapse(date)
			case tag == "table" && inTable:
				inTable = false
				out.tables = append(out.tables, rows)
				out.dates = append(out.dates, pendingDate)
				pendingDate = ""
			case inTable && tag == "tr" && rowOpen:
				rows = append(rows, row)
				row, rowOpen = nil, false
			case inTable && (tag == "td" || tag == "th") && cellOpen:
				if rowOpen {
					row = append(row, collapse(cell))
				}
				cell, cellOpen = "", false
			}
		},
		Data: func(data string) {
			if inDate {
				date += data
			} else if cellOpen {
				cell += data
			}
		},
	}
	parser.Feed(markup)
	parser.Close()
	return out, failure
}

func ParseRateTable(markup string) (asOf string, selling float64, err error) {
	parsed, err := parseRateTables(markup)
	if err != nil {
		return "", 0, err
	}
	for i, rows := range parsed.tables {
		asOf := parsed.dates[i]
		var header []string
		for _, r := range rows {
			if slices.ContainsFunc(r, func(c string) bool { return py.Lower(c) == "selling" }) {
				header = r
				break
			}
		}
		if header == nil {
			continue
		}
		column := slices.IndexFunc(header, func(c string) bool { return py.Lower(c) == "selling" })
		for _, r := range rows {
			if len(r) == 0 || !strings.Contains(py.Lower(r[0]), "united states dollar") || len(r) <= column {
				continue
			}
			selling, err := py.ParseFloat(r[column])
			if err != nil {
				return "", 0, fmt.Errorf("%w: unreadable USD selling rate %s", httpx.ErrValue, py.Repr(r[column]))
			}
			ok, err := isoDateRE.FullMatch(asOf)
			if err != nil {
				return "", 0, err
			}
			if ok == nil {
				asOf = ""
			}
			return asOf, selling, nil
		}
	}
	return "", 0, fmt.Errorf("%w: no United States Dollar row in a rate_table with a Selling column", httpx.ErrValue)
}

func StoreRate(ctx context.Context, q db.Querier, day string, selling float64, asOf string, now float64) error {
	_, err := q.ExecContext(ctx,
		"INSERT OR REPLACE INTO fx_rates (date, currency, selling, as_of, fetched_ts) VALUES (?, ?, ?, ?, ?)",
		day, USD, selling, asOf, now)
	return err
}

type Fetch func(ctx context.Context, url string) (string, error)

func getText(ctx context.Context, url string) (string, error) { return httpx.GetText(ctx, url, 0) }

type FetchedRate struct {
	Date    string  `json:"date"`
	Selling float64 `json:"selling"`
	AsOf    string  `json:"as_of"`
}

func FetchRates(ctx context.Context, q db.Querier, now float64, loc *time.Location, fetch Fetch) (*FetchedRate, error) {
	if fetch == nil {
		fetch = getText
	}
	endpoint, err := LoadCBTTURL(ctx, q)
	if err != nil {
		return nil, err
	}
	markup, err := fetch(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	asOf, selling, err := ParseRateTable(markup)
	if err != nil {
		return nil, err
	}
	day := isoDate(LocalDate(now, loc))
	if err := StoreRate(ctx, q, day, selling, asOf, now); err != nil {
		return nil, err
	}
	return &FetchedRate{Date: day, Selling: selling, AsOf: asOf}, nil
}

type rate struct {
	selling float64
	date    string
}

func rateOn(ctx context.Context, q db.Querier, day string) (*rate, error) {
	var r rate
	err := q.QueryRowContext(ctx,
		"SELECT date, selling FROM fx_rates WHERE currency = ? AND date <= ? ORDER BY date DESC LIMIT 1", USD, day).Scan(&r.date, &r.selling)
	if errors.Is(err, sql.ErrNoRows) {
		err = q.QueryRowContext(ctx,
			"SELECT date, selling FROM fx_rates WHERE currency = ? ORDER BY date ASC LIMIT 1", USD).Scan(&r.date, &r.selling)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func RateOn(ctx context.Context, q db.Querier, day string) (selling float64, rateDate string, found bool, err error) {
	r, err := rateOn(ctx, q, day)
	if err != nil || r == nil {
		return 0, "", false, err
	}
	return r.selling, r.date, true, nil
}

type StoredRate struct {
	Date      string   `json:"date"`
	Currency  string   `json:"currency"`
	Selling   float64  `json:"selling"`
	Buying    *float64 `json:"buying"`
	AsOf      string   `json:"as_of"`
	FetchedTS float64  `json:"fetched_ts"`
}

func ListRates(ctx context.Context, q db.Querier, limit int) ([]StoredRate, error) {
	rows, err := q.QueryContext(ctx, "SELECT date, currency, selling, buying, as_of, fetched_ts FROM fx_rates ORDER BY date DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (StoredRate, error) {
		var s StoredRate
		return s, r.Scan(&s.Date, &s.Currency, &s.Selling, &s.Buying, &s.AsOf, &s.FetchedTS)
	})
}
