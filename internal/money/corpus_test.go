package money

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/db/dbtest"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
	"github.com/jeiang/atrium/internal/testutil/golden"
)

var stateTables = []struct{ name, order string }{
	{"money_accounts", "id"}, {"fx_rates", "date, currency"}, {"categories", "id"}, {"payees", "id"}, {"payee_aliases", "id"},
	{"transactions", "id"}, {"transaction_sources", "id"}, {"settings", "key"}, {"agent_writes", "id"},
}

type record struct {
	Scenario string          `json:"scenario"`
	Step     int             `json:"step"`
	Op       json.RawMessage `json:"op"`
	Result   json.RawMessage `json:"result"`
	Error    *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	State json.RawMessage `json:"state"`
}

type recordedEdit struct {
	Tool   string  `json:"tool"`
	Target string  `json:"target"`
	Now    float64 `json:"now"`
}

type opHeader struct {
	Op     string        `json:"op"`
	Record *recordedEdit `json:"record"`
	Label  string        `json:"label"`
}

func loadCorpus(t *testing.T) (map[string][]record, []string, map[string]string) {
	t.Helper()
	dir := rxtest.Dir()
	f, err := os.Open(filepath.Join(dir, "money.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(nil, 1<<26)
	byScenario := map[string][]record{}
	var order []string
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if _, ok := byScenario[r.Scenario]; !ok {
			order = append(order, r.Scenario)
		}
		byScenario[r.Scenario] = append(byScenario[r.Scenario], r)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "money_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		RatePages map[string]string `json:"rate_pages"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	return byScenario, order, manifest.RatePages
}

type runner struct {
	t      *testing.T
	ctx    context.Context
	loc    *time.Location
	pages  map[string]string
	labels map[string]int64
	undo   *writes.Registry
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return v
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func optionalID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func patternsOf(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		split, err := SplitSenders(text)
		if err != nil {
			t.Fatal(err)
		}
		return split
	}
	return decode[[]string](t, raw)
}

func ratePair(selling float64, date string, found bool, err error) (any, error) {
	if err != nil || !found {
		return nil, err
	}
	return []any{selling, date}, nil
}

func (r *runner) execute(tx *sql.Tx, op json.RawMessage) (any, error) {
	ctx, t := r.ctx, r.t
	head := decode[opHeader](t, op)
	finish := func(change *Change, value any, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		var writeID any
		id, err := change.Record(ctx, tx, Context{Now: func() float64 { return head.Record.Now }}, head.Record.Tool, head.Record.Target)
		if err != nil {
			return nil, err
		}
		if id != nil {
			writeID = *id
			if head.Label != "" {
				r.labels[head.Label] = *id
			}
		}
		return map[string]any{"value": value, "write_id": writeID}, nil
	}
	today := func(raw string) time.Time {
		day, ok := ParseDate(raw)
		if !ok {
			t.Fatalf("bad corpus date %q", raw)
		}
		return day
	}
	switch head.Op {
	case "sql":
		a := decode[struct {
			Statement string `json:"statement"`
			Params    []any  `json:"params"`
		}](t, op)
		for i, p := range a.Params {
			a.Params[i] = bindable(p)
		}
		_, err := tx.ExecContext(ctx, a.Statement, a.Params...)
		return nil, err
	case "seed_tx":
		a := decode[struct {
			Item      json.RawMessage `json:"item"`
			Role      string          `json:"role"`
			MessageID int64           `json:"message_id"`
			Payee     *int64          `json:"payee"`
			Category  *string         `json:"category"`
			Now       float64         `json:"now"`
		}](t, op)
		loaded, err := py.JSONLoads(a.Item)
		if err != nil {
			return nil, err
		}
		dict := loaded.(py.Dict)
		fields, err := Derive(ctx, tx, []Source{{Role: a.Role, Item: ParseItem(dict)}})
		if err != nil {
			return nil, err
		}
		category := ""
		if a.Category != nil {
			category = *a.Category
		}
		change := NewChange()
		id, err := insertTransaction(ctx, tx, fields, a.Payee, category, a.Now, change)
		if err != nil {
			return nil, err
		}
		_, err = addSource(ctx, tx, id, a.MessageID, a.Role, dict, a.Now, change)
		return id, err
	case "add_account":
		a := decode[struct {
			Bank, Name, Kind, Currency, Last4 string
			SenderPatterns                    json.RawMessage `json:"sender_patterns"`
			Forex                             bool
			Now                               float64
		}](t, op)
		return AddAccount(ctx, tx, NewAccount{
			Bank: a.Bank, Name: a.Name, Kind: a.Kind, Currency: a.Currency, Last4: a.Last4,
			SenderPatterns: patternsOf(t, a.SenderPatterns), Forex: a.Forex,
		}, a.Now)
	case "update_account":
		a := decode[struct {
			ID             int64
			Name           *string
			Last4          *string
			SenderPatterns json.RawMessage `json:"sender_patterns"`
			Forex          *bool
		}](t, op)
		in := AccountUpdate{Name: a.Name, Last4: a.Last4, Forex: a.Forex}
		if len(a.SenderPatterns) > 0 {
			list := patternsOf(t, a.SenderPatterns)
			if list == nil {
				list = []string{}
			}
			in.SenderPatterns = &list
		}
		return UpdateAccount(ctx, tx, a.ID, in)
	case "delete_account":
		return DeleteAccount(ctx, tx, decode[struct{ ID int64 }](t, op).ID)
	case "get_account":
		return GetAccount(ctx, tx, decode[struct{ ID int64 }](t, op).ID)
	case "list_accounts":
		return ListAccounts(ctx, tx)
	case "sender_matches":
		a := decode[struct {
			AccountID  int64  `json:"account_id"`
			FromAddr   string `json:"from_addr"`
			FromDomain string `json:"from_domain"`
		}](t, op)
		account, err := GetAccount(ctx, tx, a.AccountID)
		if err != nil {
			return nil, err
		}
		return SenderMatches(account, a.FromAddr, a.FromDomain), nil
	case "store_rate":
		a := decode[struct {
			Day     string
			Selling float64
			AsOf    string `json:"as_of"`
			Now     float64
		}](t, op)
		return nil, StoreRate(ctx, tx, a.Day, a.Selling, a.AsOf, a.Now)
	case "rate_on":
		selling, date, found, err := RateOn(ctx, tx, decode[struct{ Day string }](t, op).Day)
		return ratePair(selling, date, found, err)
	case "list_rates":
		return ListRates(ctx, tx, decode[struct{ Limit int }](t, op).Limit)
	case "parse_rate_table":
		asOf, selling, err := ParseRateTable(r.pages[decode[struct{ Page string }](t, op).Page])
		if err != nil {
			return nil, err
		}
		return []any{asOf, selling}, nil
	case "fetch_rates":
		a := decode[struct {
			Page string
			Now  float64
		}](t, op)
		return FetchRates(ctx, tx, a.Now, r.loc, func(_ context.Context, url string) (string, error) {
			if url != CBTTURL {
				t.Errorf("fetched %q", url)
			}
			if a.Page == "down" {
				return "", &httpx.ServiceUnavailable{Msg: "CBTT is down"}
			}
			return r.pages[a.Page], nil
		})
	case "resolve":
		a := decode[struct {
			AccountID int64 `json:"account_id"`
			Amount    float64
			Currency  string
			Day       string
		}](t, op)
		account, err := GetAccount(ctx, tx, a.AccountID)
		if err != nil {
			return nil, err
		}
		got, err := Resolve(ctx, tx, account, a.Amount, a.Currency, a.Day)
		if err != nil {
			return nil, err
		}
		var amount, rate, rateDate any
		if got.Amount != nil {
			amount = *got.Amount
		}
		if got.Rate != nil {
			rate = *got.Rate
		}
		if got.RateDate != nil {
			rateDate = *got.RateDate
		}
		return []any{amount, rate, rateDate, got.Review}, nil
	case "normalize_raw":
		return NormalizeRaw(decode[struct{ Raw string }](t, op).Raw)
	case "create_payee":
		a := decode[struct {
			Name     string
			Category *string
			Now      float64
		}](t, op)
		category := ""
		if a.Category != nil {
			category = *a.Category
		}
		return CreatePayee(ctx, tx, a.Name, category, a.Now, nil)
	case "find_payee":
		id, err := FindPayeeByName(ctx, tx, decode[struct{ Name string }](t, op).Name)
		return optionalID(id), err
	case "payee_get":
		return GetPayee(ctx, tx, decode[struct{ ID int64 }](t, op).ID)
	case "add_alias":
		a := decode[struct {
			PayeeID int64 `json:"payee_id"`
			Pattern string
			Kind    string
		}](t, op)
		return AddAlias(ctx, tx, a.PayeeID, a.Pattern, a.Kind, nil)
	case "match_alias":
		id, err := MatchAlias(ctx, tx, decode[struct{ Normalized string }](t, op).Normalized)
		return optionalID(id), err
	case "categories":
		return Categories(ctx, tx)
	case "category_id":
		id, err := CategoryID(ctx, tx, decode[struct{ Name string }](t, op).Name)
		return optionalID(id), err
	case "add_category":
		return AddCategory(ctx, tx, decode[struct{ Name string }](t, op).Name)
	case "rename_category":
		a := decode[struct {
			ID   int64
			Name string
		}](t, op)
		return RenameCategory(ctx, tx, a.ID, a.Name)
	case "transaction":
		return GetTransaction(ctx, tx, decode[struct{ ID int64 }](t, op).ID)
	case "list_transactions":
		a := decode[struct {
			Filters struct {
				Start     string
				End       string
				AccountID int64 `json:"account_id"`
				Payee     string
				Category  string
				Kind      string
				Currency  string
			}
			Unassigned bool
			Limit      int
			Dismissed  bool
			Today      string
		}](t, op)
		return ListTransactions(ctx, tx, Filter(a.Filters), a.Unassigned, a.Limit, a.Dismissed, today(a.Today))
	case "unassigned":
		a := decode[struct {
			Limit int
			Today string
		}](t, op)
		return Unassigned(ctx, tx, a.Limit, today(a.Today))
	case "spending":
		a := decode[struct {
			Start, End, Currency string
			GroupBy              string `json:"group_by"`
			Today                string
		}](t, op)
		return Spending(ctx, tx, a.Start, a.End, a.GroupBy, a.Currency, today(a.Today))
	case "list_payees":
		a := decode[struct {
			Query string
			Limit int
		}](t, op)
		return ListPayees(ctx, tx, a.Query, a.Limit)
	case "bills":
		a := decode[struct {
			Today        string
			UpcomingDays *int `json:"upcoming_days"`
		}](t, op)
		return Bills(ctx, tx, today(a.Today), a.UpcomingDays)
	case "period_bounds":
		a := decode[struct{ Period, Today string }](t, op)
		start, end, err := PeriodBounds(a.Period, today(a.Today))
		if err != nil {
			return nil, err
		}
		return []any{start, end}, nil
	case "tracked_since":
		since, err := TrackedSince(ctx, tx)
		return nilIfEmpty(since), err
	case "set_tracked_since":
		value, err := SetTrackedSince(ctx, tx, decode[struct{ Value string }](t, op).Value)
		return nilIfEmpty(value), err
	case "undo":
		a := decode[struct {
			Of      string `json:"of"`
			WriteID int64  `json:"write_id"`
			Now     float64
		}](t, op)
		id := a.WriteID
		if a.Of != "" {
			id = r.labels[a.Of]
		}
		return r.undo.Undo(ctx, tx, id, func() float64 { return a.Now })
	case "transaction_merge":
		keep, change, err := TransactionMerge(ctx, tx, decode[struct{ IDs []int64 }](t, op).IDs)
		return finish(change, keep, err)
	case "transaction_split":
		made, change, err := TransactionSplit(ctx, tx, decode[struct{ ID int64 }](t, op).ID, head.Record.Now)
		return finish(change, made, err)
	case "transaction_set_account":
		a := decode[struct {
			TransactionID int64 `json:"transaction_id"`
			AccountID     int64 `json:"account_id"`
		}](t, op)
		change, err := TransactionSetAccount(ctx, tx, a.TransactionID, a.AccountID)
		return finish(change, nil, err)
	case "transaction_set_category":
		a := decode[struct {
			TransactionID int64  `json:"transaction_id"`
			CategoryID    *int64 `json:"category_id"`
		}](t, op)
		change, err := TransactionSetCategory(ctx, tx, a.TransactionID, a.CategoryID)
		return finish(change, nil, err)
	case "transaction_set_currency":
		a := decode[struct {
			TransactionID int64 `json:"transaction_id"`
			Currency      string
		}](t, op)
		change, err := TransactionSetCurrency(ctx, tx, a.TransactionID, a.Currency)
		return finish(change, nil, err)
	case "transaction_dismiss":
		a := decode[struct {
			TransactionID int64 `json:"transaction_id"`
			Reason        string
		}](t, op)
		change, err := TransactionDismiss(ctx, tx, a.TransactionID, a.Reason)
		return finish(change, nil, err)
	case "transactions_dismiss_before":
		a := decode[struct{ Day, Reason string }](t, op)
		count, change, err := TransactionsDismissBefore(ctx, tx, a.Day, a.Reason)
		return finish(change, count, err)
	case "payee_rename":
		a := decode[struct {
			PayeeID int64 `json:"payee_id"`
			Name    string
		}](t, op)
		change, err := PayeeRename(ctx, tx, a.PayeeID, a.Name)
		return finish(change, nil, err)
	case "payee_merge":
		a := decode[struct {
			SourceID int64 `json:"source_id"`
			TargetID int64 `json:"target_id"`
		}](t, op)
		change, err := PayeeMerge(ctx, tx, a.SourceID, a.TargetID)
		return finish(change, nil, err)
	case "payee_alias_add":
		a := decode[struct {
			PayeeID int64 `json:"payee_id"`
			Pattern string
			Kind    string
		}](t, op)
		id, change, err := PayeeAliasAdd(ctx, tx, a.PayeeID, a.Pattern, a.Kind)
		return finish(change, id, err)
	case "payee_alias_delete":
		change, err := PayeeAliasDelete(ctx, tx, decode[struct {
			AliasID int64 `json:"alias_id"`
		}](t, op).AliasID)
		return finish(change, nil, err)
	case "payee_set_category":
		a := decode[struct {
			PayeeID    int64  `json:"payee_id"`
			CategoryID *int64 `json:"category_id"`
		}](t, op)
		change, err := PayeeSetCategory(ctx, tx, a.PayeeID, a.CategoryID)
		return finish(change, nil, err)
	case "payee_set_override":
		a := decode[struct {
			PayeeID  int64   `json:"payee_id"`
			Override *string `json:"override"`
		}](t, op)
		change, err := PayeeSetOverride(ctx, tx, a.PayeeID, a.Override)
		return finish(change, nil, err)
	}
	t.Fatalf("unknown corpus op %q", head.Op)
	return nil, nil
}

func classify(err error) (string, string) {
	var ledger LedgerError
	var undo writes.UndoError
	var unavailable *httpx.ServiceUnavailable
	var exc *py.Exception
	switch {
	case errors.As(err, &ledger):
		return "LedgerError", string(ledger)
	case errors.As(err, &undo):
		return "UndoError", string(undo)
	case errors.As(err, &unavailable):
		return "ServiceUnavailable", unavailable.Msg
	case errors.Is(err, httpx.ErrValue):
		return "ValueError", httpx.Message(err)
	case errors.As(err, &exc):
		return exc.Class, exc.Msg
	case strings.Contains(err.Error(), "constraint failed"):
		return "IntegrityError", err.Error()
	}
	return "RuntimeError", err.Error()
}

func dumpState(t *testing.T, ctx context.Context, d *db.DB) []byte {
	t.Helper()
	out := map[string]any{}
	for _, table := range stateTables {
		rows, err := d.Read.QueryContext(ctx, "SELECT * FROM "+table.name+" ORDER BY "+table.order)
		if err != nil {
			t.Fatal(err)
		}
		list, err := db.Collect(rows, func(r *sql.Rows) (map[string]any, error) {
			columns, err := r.Columns()
			if err != nil {
				return nil, err
			}
			values := make([]any, len(columns))
			targets := make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := r.Scan(targets...); err != nil {
				return nil, err
			}
			row := make(map[string]any, len(columns))
			for i, c := range columns {
				if b, ok := values[i].([]byte); ok {
					values[i] = string(b)
				}
				row[c] = values[i]
			}
			return row, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out[table.name] = list
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCorpus(t *testing.T) {
	scenarios, order, pages := loadCorpus(t)
	loc, err := config.Zone("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range order {
		t.Run(name, func(t *testing.T) {
			d := dbtest.Schema(t)
			r := &runner{t: t, ctx: t.Context(), loc: loc, pages: pages, labels: map[string]int64{}, undo: writes.NewRegistry()}
			RegisterUndoer(r.undo)
			for _, rec := range scenarios[name] {
				var result any
				opErr := d.WithTx(r.ctx, func(tx *sql.Tx) error {
					var err error
					result, err = r.execute(tx, rec.Op)
					return err
				})
				if rec.Error != nil {
					if opErr == nil {
						t.Errorf("step %d %s: no error, Python raised %s: %s", rec.Step, rec.Op, rec.Error.Type, rec.Error.Message)
						continue
					}
					kind, message := classify(opErr)
					if kind != rec.Error.Type || (kind != "IntegrityError" && message != rec.Error.Message) ||
						(kind == "IntegrityError" && !strings.Contains(message, rec.Error.Message)) {
						t.Errorf("step %d %s: error %s: %s, Python %s: %s", rec.Step, rec.Op, kind, message, rec.Error.Type, rec.Error.Message)
					}
				} else if opErr != nil {
					t.Errorf("step %d %s: %v", rec.Step, rec.Op, opErr)
					continue
				} else {
					got, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					if rec.Result == nil {
						rec.Result = json.RawMessage("null")
					}
					diffs, err := golden.Diff(rec.Result, got)
					if err != nil {
						t.Fatalf("step %d: %v", rec.Step, err)
					}
					if len(diffs) > 0 {
						t.Errorf("step %d %s:\n%s", rec.Step, rec.Op, strings.Join(diffs, "\n"))
					}
				}
				if rec.State != nil {
					diffs, err := golden.Diff(rec.State, dumpState(t, r.ctx, d))
					if err != nil {
						t.Fatal(err)
					}
					if len(diffs) > 0 {
						t.Errorf("step %d %s state:\n%s", rec.Step, rec.Op, strings.Join(diffs[:min(len(diffs), 20)], "\n"))
					}
				}
			}
		})
	}
}

func TestPatterns(t *testing.T) {
	patterns := map[string]*rx.Regexp{
		"src/atrium/money.py:STORE_NUMBER":              storeNumberRE,
		"src/atrium/money.py:normalize_raw:06157e46":    nonDescriptorRE,
		"src/atrium/money.py:_patterns:875c02cb":        patternSplitRE,
		"src/atrium/money.py:add_account:78701180":      nonDigitRE,
		"src/atrium/money.py:update_account:78701180":   nonDigitRE,
		"src/atrium/money.py:parse_rate_table:d072aa40": isoDateRE,
		"src/atrium/money.py:period_bounds:53692041":    periodMonthRE,
		"src/atrium/money.py:period_bounds:98811e5e":    periodYearRE,
		"src/atrium/money.py:period_bounds:23691e9c":    periodDaysRE,
		"src/atrium/money.py:period_bounds:46dc6db4":    periodRangeRE,
	}
	c := rxtest.Load(t)
	for _, p := range c.Patterns {
		var flags rx.Flags
		switch {
		case strings.HasPrefix(p.ID, "src/atrium/money.py:match_alias:"):
			flags = rx.IgnoreCase
		case strings.HasPrefix(p.ID, "src/atrium/money.py:add_alias:"):
		default:
			continue
		}
		re, err := rx.Compile(p.Pattern, flags)
		if p.Error != "" {
			if err == nil || err.Error() != p.Error {
				t.Errorf("%s: %q: error %v, Python %q", p.ID, p.Pattern, err, p.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %q: %v", p.ID, p.Pattern, err)
			continue
		}
		patterns[p.ID] = re
	}
	rxtest.Run(t, patterns)
}
