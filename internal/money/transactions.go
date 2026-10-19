package money

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Item struct {
	Kind            string
	PayeeRaw        string
	Amount          float64
	Currency        string
	Date            string
	Bank            string
	AccountLast4    string
	AccountAmount   *float64
	AccountCurrency string
	StatementTotal  *float64
	Description     string
	Subscription    bool
	Cadence         string
	AccountID       *int64
}

func (i Item) Dict() py.Dict {
	var accountAmount, statementTotal, accountID any
	if i.AccountAmount != nil {
		accountAmount = *i.AccountAmount
	}
	if i.StatementTotal != nil {
		statementTotal = *i.StatementTotal
	}
	if i.AccountID != nil {
		accountID = *i.AccountID
	}
	return py.Dict{
		{Key: "kind", Value: i.Kind}, {Key: "payee_raw", Value: i.PayeeRaw}, {Key: "amount", Value: i.Amount},
		{Key: "currency", Value: i.Currency}, {Key: "date", Value: i.Date}, {Key: "bank", Value: i.Bank},
		{Key: "account_last4", Value: i.AccountLast4}, {Key: "account_amount", Value: accountAmount},
		{Key: "account_currency", Value: i.AccountCurrency}, {Key: "statement_total", Value: statementTotal},
		{Key: "description", Value: i.Description}, {Key: "subscription", Value: i.Subscription},
		{Key: "cadence", Value: i.Cadence}, {Key: "account_id", Value: accountID},
	}
}

func dictString(d py.Dict, key string) string {
	v, _ := d.Get(key)
	s, _ := v.(string)
	return s
}

func dictFloat(d py.Dict, key string) *float64 {
	v, _ := d.Get(key)
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int64:
		f = float64(n)
	case *big.Int:
		f, _ = new(big.Float).SetInt(n).Float64()
	default:
		return nil
	}
	return &f
}

func dictInt(d py.Dict, key string) *int64 {
	v, _ := d.Get(key)
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		i := int64(n)
		return &i
	}
	return nil
}

func ParseItem(d py.Dict) Item {
	item := Item{
		Kind: dictString(d, "kind"), PayeeRaw: dictString(d, "payee_raw"), Currency: dictString(d, "currency"),
		Date: dictString(d, "date"), Bank: dictString(d, "bank"), AccountLast4: dictString(d, "account_last4"),
		AccountAmount: dictFloat(d, "account_amount"), AccountCurrency: dictString(d, "account_currency"),
		StatementTotal: dictFloat(d, "statement_total"), Description: dictString(d, "description"),
		Cadence: dictString(d, "cadence"), AccountID: dictInt(d, "account_id"),
	}
	if amount := dictFloat(d, "amount"); amount != nil {
		item.Amount = *amount
	}
	v, _ := d.Get("subscription")
	item.Subscription = py.Truthy(v)
	return item
}

type Source struct {
	ID            int64
	TransactionID int64
	MessageID     int64
	Role          string
	Extracted     string
	TS            float64
	Raw           py.Dict
	Item          Item
}

func sourcesOf(ctx context.Context, q db.Querier, txID int64) ([]Source, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT id, transaction_id, message_id, role, extracted, ts FROM transaction_sources WHERE transaction_id = ? ORDER BY id", txID)
	if err != nil {
		return nil, err
	}
	found, err := db.Collect(rows, func(r *sql.Rows) (Source, error) {
		var s Source
		return s, r.Scan(&s.ID, &s.TransactionID, &s.MessageID, &s.Role, &s.Extracted, &s.TS)
	})
	if err != nil {
		return nil, err
	}
	for i := range found {
		decoded, err := py.JSONLoads([]byte(found[i].Extracted))
		if err != nil {
			return nil, err
		}
		raw, ok := decoded.(py.Dict)
		if !ok {
			return nil, fmt.Errorf("transaction source %d: extracted is not an object", found[i].ID)
		}
		found[i].Raw, found[i].Item = raw, ParseItem(raw)
	}
	return found, nil
}

type Fields struct {
	AccountID        *int64
	Kind             string
	Date             string
	PayeeRaw         string
	Description      string
	OriginalAmount   float64
	OriginalCurrency string
	Amount           *float64
	Currency         *string
	ComputedAmount   *float64
	Rate             *float64
	RateDate         *string
	Review           string
}

func truthyID(id *int64) bool { return id != nil && *id != 0 }

func Derive(ctx context.Context, q db.Querier, sources []Source) (Fields, error) {
	var alert, receipt *Source
	for i := range sources {
		switch {
		case alert == nil && sources[i].Role == BankAlert:
			alert = &sources[i]
		case receipt == nil && sources[i].Role == Receipt:
			receipt = &sources[i]
		}
	}
	main := alert
	if main == nil {
		main = receipt
	}
	if main == nil {
		main = &sources[0]
	}
	a, r := main.Item, main.Item
	if receipt != nil {
		r = receipt.Item
	}
	var accountID *int64
	for _, s := range []*Source{alert, receipt, main} {
		if s != nil && truthyID(s.Item.AccountID) {
			accountID = s.Item.AccountID
			break
		}
	}
	fields := Fields{
		AccountID: accountID, Kind: a.Kind, Date: a.Date, PayeeRaw: a.PayeeRaw, Description: r.Description,
		OriginalAmount: round2(r.Amount), OriginalCurrency: r.Currency,
	}
	if fields.PayeeRaw == "" {
		fields.PayeeRaw = r.PayeeRaw
	}
	if fields.Description == "" {
		fields.Description = a.Description
	}
	if accountID == nil {
		return fields, nil
	}
	account, err := GetAccount(ctx, q, *accountID)
	if err != nil {
		return Fields{}, err
	}
	if account == nil {
		fields.Review = "account was deleted"
		return fields, nil
	}
	resolved, err := Resolve(ctx, q, account, fields.OriginalAmount, fields.OriginalCurrency, fields.Date)
	if err != nil {
		return Fields{}, err
	}
	currency := account.Currency
	fields.Currency, fields.Rate, fields.RateDate, fields.Review, fields.Amount = &currency, resolved.Rate, resolved.RateDate, resolved.Review, resolved.Amount
	var stated *float64
	switch {
	case alert != nil && a.AccountCurrency == account.Currency:
		stated = a.AccountAmount
	case alert != nil && a.Currency == account.Currency:
		amount := a.Amount
		stated = &amount
	}
	if stated != nil && resolved.Review == "" && (resolved.Amount == nil || round2(*stated) != *resolved.Amount) {
		fields.ComputedAmount = resolved.Amount
		fields.Amount = roundPtr(stated)
	}
	return fields, nil
}

func insertTransaction(ctx context.Context, q db.Querier, f Fields, payeeID *int64, category string, now float64, change *Change) (int64, error) {
	var categoryID *int64
	if category != "" {
		var err error
		if categoryID, err = CategoryID(ctx, q, category); err != nil {
			return 0, err
		}
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO transactions (account_id, kind, date, payee_id, payee_raw, description, original_amount, original_currency, "+
			"amount, currency, computed_amount, rate, rate_date, category_id, review, created_ts) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		f.AccountID, f.Kind, f.Date, payeeID, f.PayeeRaw, f.Description, f.OriginalAmount, f.OriginalCurrency,
		f.Amount, f.Currency, f.ComputedAmount, f.Rate, f.RateDate, categoryID, f.Review, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	change.Created("transactions", id)
	return id, nil
}

func updateFields(ctx context.Context, q db.Querier, txID int64, f Fields) error {
	_, err := q.ExecContext(ctx,
		"UPDATE transactions SET account_id = ?, kind = ?, date = ?, payee_raw = ?, description = ?, original_amount = ?, "+
			"original_currency = ?, amount = ?, currency = ?, computed_amount = ?, rate = ?, rate_date = ?, review = ? WHERE id = ?",
		f.AccountID, f.Kind, f.Date, f.PayeeRaw, f.Description, f.OriginalAmount, f.OriginalCurrency, f.Amount, f.Currency,
		f.ComputedAmount, f.Rate, f.RateDate, f.Review, txID)
	return err
}

func addSource(ctx context.Context, q db.Querier, txID, messageID int64, role string, item py.Dict, now float64, change *Change) (int64, error) {
	res, err := q.ExecContext(ctx,
		"INSERT INTO transaction_sources (transaction_id, message_id, role, extracted, ts) VALUES (?, ?, ?, ?, ?)",
		txID, messageID, role, py.JSONDumps(item, py.JSONOpts{SortKeys: true}), now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	change.Created("transaction_sources", id)
	return id, nil
}

func rederive(ctx context.Context, q db.Querier, txID int64) error {
	sources, err := sourcesOf(ctx, q, txID)
	if err != nil || len(sources) == 0 {
		return err
	}
	fields, err := Derive(ctx, q, sources)
	if err != nil {
		return err
	}
	return updateFields(ctx, q, txID, fields)
}

func unlink(ctx context.Context, q db.Querier, txID int64, change *Change) error {
	var other *int64
	if err := q.QueryRowContext(ctx, "SELECT transfer_id FROM transactions WHERE id = ?", txID).Scan(&other); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !truthyID(other) {
		return nil
	}
	if err := change.Touch(ctx, q, "transactions", *other); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "UPDATE transactions SET transfer_id = NULL WHERE id = ?", *other); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE transactions SET transfer_id = NULL WHERE id = ?", txID)
	return err
}

func deleteTransaction(ctx context.Context, q db.Querier, txID int64, change *Change) error {
	if err := change.TouchTransaction(ctx, q, txID); err != nil {
		return err
	}
	if err := unlink(ctx, q, txID, change); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM transaction_sources WHERE transaction_id = ?", txID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM transactions WHERE id = ?", txID)
	return err
}

func mergeInto(ctx context.Context, q db.Querier, keepID, dropID int64, change *Change) error {
	if err := change.TouchTransaction(ctx, q, keepID); err != nil {
		return err
	}
	if err := change.TouchTransaction(ctx, q, dropID); err != nil {
		return err
	}
	if err := unlink(ctx, q, dropID, change); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "UPDATE transaction_sources SET transaction_id = ? WHERE transaction_id = ?", keepID, dropID); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, "DELETE FROM transactions WHERE id = ?", dropID); err != nil {
		return err
	}
	return rederive(ctx, q, keepID)
}

type SourceRef struct {
	MessageID int64  `json:"message_id"`
	Role      string `json:"role"`
}

type TransactionRow struct {
	ID               int64    `json:"id"`
	AccountID        *int64   `json:"account_id"`
	Kind             string   `json:"kind"`
	Date             string   `json:"date"`
	PayeeID          *int64   `json:"payee_id"`
	PayeeRaw         string   `json:"payee_raw"`
	Description      string   `json:"description"`
	OriginalAmount   float64  `json:"original_amount"`
	OriginalCurrency string   `json:"original_currency"`
	Amount           *float64 `json:"amount"`
	Currency         *string  `json:"currency"`
	ComputedAmount   *float64 `json:"computed_amount"`
	Rate             *float64 `json:"rate"`
	RateDate         *string  `json:"rate_date"`
	CategoryID       *int64   `json:"category_id"`
	TransferID       *int64   `json:"transfer_id"`
	Review           string   `json:"review"`
	Dismissed        string   `json:"dismissed"`
	CreatedTS        float64  `json:"created_ts"`
}

func (t *TransactionRow) targets() []any {
	return []any{
		&t.ID, &t.AccountID, &t.Kind, &t.Date, &t.PayeeID, &t.PayeeRaw, &t.Description, &t.OriginalAmount, &t.OriginalCurrency,
		&t.Amount, &t.Currency, &t.ComputedAmount, &t.Rate, &t.RateDate, &t.CategoryID, &t.TransferID, &t.Review, &t.Dismissed, &t.CreatedTS,
	}
}

const transactionColumns = "t.id, t.account_id, t.kind, t.date, t.payee_id, t.payee_raw, t.description, t.original_amount, " +
	"t.original_currency, t.amount, t.currency, t.computed_amount, t.rate, t.rate_date, t.category_id, t.transfer_id, t.review, " +
	"t.dismissed, t.created_ts"

func displayPayee(payee *string, raw, kind string) string {
	switch {
	case payee != nil && *payee != "":
		return *payee
	case raw != "":
		return raw
	}
	return replaceUnderscores(kind)
}

func replaceUnderscores(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c == '_' {
			out[i] = ' '
		}
	}
	return string(out)
}

type Transaction struct {
	TransactionRow
	Payee           *string     `json:"payee"`
	Account         *string     `json:"account"`
	AccountCurrency *string     `json:"account_currency"`
	Category        *string     `json:"category"`
	Sources         []SourceRef `json:"sources"`
	DisplayPayee    string      `json:"display_payee"`
}

func GetTransaction(ctx context.Context, q db.Querier, txID int64) (*Transaction, error) {
	var t Transaction
	err := q.QueryRowContext(ctx,
		"SELECT "+transactionColumns+", p.name, a.name, a.currency, COALESCE(c.name, pc.name) FROM transactions t "+
			"LEFT JOIN payees p ON p.id = t.payee_id LEFT JOIN money_accounts a ON a.id = t.account_id "+
			"LEFT JOIN categories c ON c.id = t.category_id LEFT JOIN categories pc ON pc.id = p.category_id WHERE t.id = ?", txID).
		Scan(append(t.targets(), &t.Payee, &t.Account, &t.AccountCurrency, &t.Category)...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sources, err := sourcesOf(ctx, q, txID)
	if err != nil {
		return nil, err
	}
	t.Sources = make([]SourceRef, len(sources))
	for i, s := range sources {
		t.Sources[i] = SourceRef{MessageID: s.MessageID, Role: s.Role}
	}
	t.DisplayPayee = displayPayee(t.Payee, t.PayeeRaw, t.Kind)
	return &t, nil
}

func need(ctx context.Context, q db.Querier, table string, id int64, what string) (Row, error) {
	row, err := rowOf(ctx, q, table, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, LedgerError(fmt.Sprintf("unknown %s %d", what, id))
	}
	return row, nil
}

func hasRole(sources []Source, role string) bool {
	return slices.ContainsFunc(sources, func(s Source) bool { return s.Role == role })
}

func TransactionMerge(ctx context.Context, q db.Querier, ids []int64) (int64, *Change, error) {
	ids = slices.Clone(ids)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) < 2 {
		return 0, nil, LedgerError("merge needs at least two transaction ids")
	}
	for _, id := range ids {
		if _, err := need(ctx, q, "transactions", id, "transaction"); err != nil {
			return 0, nil, err
		}
	}
	var withAlert []int64
	for _, id := range ids {
		sources, err := sourcesOf(ctx, q, id)
		if err != nil {
			return 0, nil, err
		}
		if hasRole(sources, BankAlert) {
			withAlert = append(withAlert, id)
		}
	}
	if len(withAlert) > 1 {
		return 0, nil, LedgerError("only one of the transactions may come from a bank alert")
	}
	keep := ids[0]
	if len(withAlert) == 1 {
		keep = withAlert[0]
	}
	change := NewChange()
	for _, id := range ids {
		if id != keep {
			if err := mergeInto(ctx, q, keep, id, change); err != nil {
				return 0, nil, err
			}
		}
	}
	return keep, change, nil
}

func TransactionSplit(ctx context.Context, q db.Querier, txID int64, now float64) ([]int64, *Change, error) {
	tx, err := need(ctx, q, "transactions", txID, "transaction")
	if err != nil {
		return nil, nil, err
	}
	sources, err := sourcesOf(ctx, q, txID)
	if err != nil {
		return nil, nil, err
	}
	if len(sources) < 2 {
		return nil, nil, LedgerError("transaction has a single source; nothing to split")
	}
	change := NewChange()
	if err := deleteTransaction(ctx, q, txID, change); err != nil {
		return nil, nil, err
	}
	var payeeID *int64
	if _, ok := tx["payee_id"].(int64); ok {
		id := rowInt(tx, "payee_id")
		payeeID = &id
	}
	made := make([]int64, 0, len(sources))
	for _, s := range sources {
		fields, err := Derive(ctx, q, []Source{s})
		if err != nil {
			return nil, nil, err
		}
		category := ""
		switch {
		case fields.Kind == ATM:
			category = Cash
		case contains(TransferKinds, fields.Kind):
			category = TransferCategory
		}
		newID, err := insertTransaction(ctx, q, fields, payeeID, category, now, change)
		if err != nil {
			return nil, nil, err
		}
		if _, err := addSource(ctx, q, newID, s.MessageID, s.Role, s.Raw, now, change); err != nil {
			return nil, nil, err
		}
		made = append(made, newID)
	}
	return made, change, nil
}

func rewriteSources(ctx context.Context, q db.Querier, txID int64, key string, value any) error {
	sources, err := sourcesOf(ctx, q, txID)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if _, err := q.ExecContext(ctx, "UPDATE transaction_sources SET extracted = ? WHERE id = ?",
			py.JSONDumps(s.Raw.Set(key, value), py.JSONOpts{SortKeys: true}), s.ID); err != nil {
			return err
		}
	}
	return nil
}

func TransactionSetAccount(ctx context.Context, q db.Querier, txID, accountID int64) (*Change, error) {
	tx, err := need(ctx, q, "transactions", txID, "transaction")
	if err != nil {
		return nil, err
	}
	if rowString(tx, "kind") == FXAdjustment {
		return nil, LedgerError("an FX adjustment belongs to the card it was computed for")
	}
	account, err := GetAccount(ctx, q, accountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, LedgerError(fmt.Sprintf("unknown account %d", accountID))
	}
	change := NewChange()
	if err := change.TouchTransaction(ctx, q, txID); err != nil {
		return nil, err
	}
	if err := unlink(ctx, q, txID, change); err != nil {
		return nil, err
	}
	if err := rewriteSources(ctx, q, txID, "account_id", accountID); err != nil {
		return nil, err
	}
	return change, rederive(ctx, q, txID)
}

func TransactionSetCategory(ctx context.Context, q db.Querier, txID int64, categoryID *int64) (*Change, error) {
	if _, err := need(ctx, q, "transactions", txID, "transaction"); err != nil {
		return nil, err
	}
	if categoryID != nil {
		if _, err := need(ctx, q, "categories", *categoryID, "category"); err != nil {
			return nil, err
		}
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "transactions", txID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "UPDATE transactions SET category_id = ? WHERE id = ?", categoryID, txID)
	return change, err
}

func TransactionSetCurrency(ctx context.Context, q db.Querier, txID int64, currency string) (*Change, error) {
	tx, err := need(ctx, q, "transactions", txID, "transaction")
	if err != nil {
		return nil, err
	}
	if rowString(tx, "kind") == FXAdjustment {
		return nil, LedgerError("an FX adjustment is always in the card's currency")
	}
	currency = py.Upper(py.Strip(currency))
	if !contains(TxCurrencies, currency) {
		return nil, LedgerError("currency must be one of " + joinList(TxCurrencies))
	}
	change := NewChange()
	if err := change.TouchTransaction(ctx, q, txID); err != nil {
		return nil, err
	}
	if err := rewriteSources(ctx, q, txID, "currency", currency); err != nil {
		return nil, err
	}
	return change, rederive(ctx, q, txID)
}

func TransactionDismiss(ctx context.Context, q db.Querier, txID int64, reason string) (*Change, error) {
	if _, err := need(ctx, q, "transactions", txID, "transaction"); err != nil {
		return nil, err
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "transactions", txID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "UPDATE transactions SET dismissed = ? WHERE id = ?", clip(reason, 80), txID)
	return change, err
}

func TransactionsDismissBefore(ctx context.Context, q db.Querier, day, reason string) (int, *Change, error) {
	parsed, ok := ParseDate(py.Strip(day))
	if !ok {
		return 0, nil, LedgerError("date must be YYYY-MM-DD")
	}
	day = isoDate(parsed)
	change := NewChange()
	ids, err := queryIDs(ctx, q, "SELECT id FROM transactions WHERE dismissed = '' AND date < ?", day)
	if err != nil {
		return 0, nil, err
	}
	if err := change.Touch(ctx, q, "transactions", ids...); err != nil {
		return 0, nil, err
	}
	if reason == "" {
		reason = "before " + day
	}
	_, err = q.ExecContext(ctx, "UPDATE transactions SET dismissed = ? WHERE dismissed = '' AND date < ?", clip(reason, 80), day)
	return len(ids), change, err
}

func PayeeRename(ctx context.Context, q db.Querier, payeeID int64, name string) (*Change, error) {
	if _, err := need(ctx, q, "payees", payeeID, "payee"); err != nil {
		return nil, err
	}
	name = clip(name, 80)
	if name == "" {
		return nil, LedgerError("name is required")
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "payees", payeeID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "UPDATE payees SET name = ? WHERE id = ?", name, payeeID)
	return change, err
}

func PayeeMerge(ctx context.Context, q db.Querier, sourceID, targetID int64) (*Change, error) {
	if sourceID == targetID {
		return nil, LedgerError("source and target are the same payee")
	}
	source, err := need(ctx, q, "payees", sourceID, "payee")
	if err != nil {
		return nil, err
	}
	if _, err := need(ctx, q, "payees", targetID, "payee"); err != nil {
		return nil, err
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "payees", sourceID, targetID); err != nil {
		return nil, err
	}
	aliasIDs, err := queryIDs(ctx, q, "SELECT id FROM payee_aliases WHERE payee_id = ?", sourceID)
	if err != nil {
		return nil, err
	}
	if err := change.Touch(ctx, q, "payee_aliases", aliasIDs...); err != nil {
		return nil, err
	}
	txIDs, err := queryIDs(ctx, q, "SELECT id FROM transactions WHERE payee_id = ?", sourceID)
	if err != nil {
		return nil, err
	}
	if err := change.Touch(ctx, q, "transactions", txIDs...); err != nil {
		return nil, err
	}
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{"UPDATE payee_aliases SET payee_id = ? WHERE payee_id = ?", []any{targetID, sourceID}},
		{"UPDATE transactions SET payee_id = ? WHERE payee_id = ?", []any{targetID, sourceID}},
		{"UPDATE payees SET subscription_seen = MAX(subscription_seen, ?), cadence = CASE WHEN cadence = '' THEN ? ELSE cadence END, " +
			"recurring_override = COALESCE(recurring_override, ?) WHERE id = ?",
			[]any{source["subscription_seen"], source["cadence"], source["recurring_override"], targetID}},
		{"DELETE FROM payees WHERE id = ?", []any{sourceID}},
	} {
		if _, err := q.ExecContext(ctx, stmt.query, stmt.args...); err != nil {
			return nil, err
		}
	}
	return change, nil
}

func PayeeAliasAdd(ctx context.Context, q db.Querier, payeeID int64, pattern, kind string) (int64, *Change, error) {
	if _, err := need(ctx, q, "payees", payeeID, "payee"); err != nil {
		return 0, nil, err
	}
	change := NewChange()
	id, err := AddAlias(ctx, q, payeeID, pattern, kind, change)
	if err != nil {
		return 0, nil, err
	}
	return id, change, nil
}

func PayeeAliasDelete(ctx context.Context, q db.Querier, aliasID int64) (*Change, error) {
	if _, err := need(ctx, q, "payee_aliases", aliasID, "alias"); err != nil {
		return nil, err
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "payee_aliases", aliasID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM payee_aliases WHERE id = ?", aliasID)
	return change, err
}

func PayeeSetCategory(ctx context.Context, q db.Querier, payeeID int64, categoryID *int64) (*Change, error) {
	if _, err := need(ctx, q, "payees", payeeID, "payee"); err != nil {
		return nil, err
	}
	if categoryID != nil {
		if _, err := need(ctx, q, "categories", *categoryID, "category"); err != nil {
			return nil, err
		}
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "payees", payeeID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "UPDATE payees SET category_id = ? WHERE id = ?", categoryID, payeeID)
	return change, err
}

func PayeeSetOverride(ctx context.Context, q db.Querier, payeeID int64, override *string) (*Change, error) {
	if override != nil && !contains(Overrides, *override) {
		return nil, LedgerError("override must be empty or one of " + joinList(Overrides))
	}
	if _, err := need(ctx, q, "payees", payeeID, "payee"); err != nil {
		return nil, err
	}
	change := NewChange()
	if err := change.Touch(ctx, q, "payees", payeeID); err != nil {
		return nil, err
	}
	_, err := q.ExecContext(ctx, "UPDATE payees SET recurring_override = ? WHERE id = ?", override, payeeID)
	return change, err
}
