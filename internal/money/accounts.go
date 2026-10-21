package money

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

type Account struct {
	ID             int64    `json:"id"`
	Bank           string   `json:"bank"`
	Name           string   `json:"name"`
	Kind           string   `json:"kind"`
	Currency       string   `json:"currency"`
	Last4          string   `json:"last4"`
	SenderPatterns []string `json:"sender_patterns"`
	Forex          bool     `json:"forex"`
	CreatedTS      float64  `json:"created_ts"`
	BankLabel      string   `json:"bank_label"`
}

const accountColumns = "id, bank, name, kind, currency, last4, sender_patterns, forex, created_ts"

func scanAccount(r interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var patterns string
	var forex int64
	if err := r.Scan(&a.ID, &a.Bank, &a.Name, &a.Kind, &a.Currency, &a.Last4, &patterns, &forex, &a.CreatedTS); err != nil {
		return a, err
	}
	if err := json.Unmarshal([]byte(patterns), &a.SenderPatterns); err != nil {
		return a, err
	}
	if a.SenderPatterns == nil {
		a.SenderPatterns = []string{}
	}
	a.Forex = forex != 0
	a.BankLabel = a.Bank
	return a, nil
}

func ListAccounts(ctx context.Context, q db.Querier) ([]Account, error) {
	banks, err := LoadBanks(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, "SELECT "+accountColumns+" FROM money_accounts ORDER BY bank, name")
	if err != nil {
		return nil, err
	}
	accounts, err := db.Collect(rows, func(r *sql.Rows) (Account, error) { return scanAccount(r) })
	for i := range accounts {
		accounts[i].labelFrom(banks)
	}
	return accounts, err
}

func GetAccount(ctx context.Context, q db.Querier, id int64) (*Account, error) {
	a, err := scanAccount(q.QueryRowContext(ctx, "SELECT "+accountColumns+" FROM money_accounts WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	banks, err := LoadBanks(ctx, q)
	if err != nil {
		return nil, err
	}
	a.labelFrom(banks)
	return &a, nil
}

func SplitSenders(text string) ([]string, error) { return patternSplitRE.Split(text, 0) }

func normalizePatterns(values []string) []string {
	set := map[string]bool{}
	for _, p := range values {
		if p != "" && py.Strip(p) != "" {
			set[py.Lower(py.Strip(p))] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

type NewAccount struct {
	Bank           string
	Name           string
	Kind           string
	Currency       string
	Last4          string
	SenderPatterns []string
	Forex          bool
}

func AddAccount(ctx context.Context, q db.Querier, in NewAccount, now float64) (int64, error) {
	banks, err := LoadBanks(ctx, q)
	if err != nil {
		return 0, err
	}
	bankIDs := BankIDs(banks)
	if !contains(bankIDs, in.Bank) {
		return 0, LedgerError("bank must be one of " + joinList(bankIDs))
	}
	if !contains(AccountKinds, in.Kind) {
		return 0, LedgerError("kind must be one of " + joinList(AccountKinds))
	}
	currencies, _, err := LoadCurrencies(ctx, q)
	if err != nil {
		return 0, err
	}
	if !contains(currencies, in.Currency) {
		return 0, LedgerError("currency must be one of " + joinList(currencies))
	}
	name := clip(in.Name, 60)
	if name == "" {
		return 0, LedgerError("name is required")
	}
	last4, err := lastDigits(in.Last4, 4)
	if err != nil {
		return 0, err
	}
	senders := in.SenderPatterns
	if senders == nil {
		senders = senderDefaults(banks, in.Bank)
	}
	res, err := q.ExecContext(ctx,
		"INSERT INTO money_accounts (bank, name, kind, currency, last4, sender_patterns, forex, created_ts) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		in.Bank, name, in.Kind, in.Currency, last4, py.JSONDumps(normalizePatterns(senders)), boolInt(in.Forex), now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type AccountUpdate struct {
	Name           *string
	Last4          *string
	SenderPatterns *[]string
	Forex          *bool
}

func UpdateAccount(ctx context.Context, q db.Querier, id int64, in AccountUpdate) (bool, error) {
	var sets []string
	var args []any
	if in.Name != nil {
		name := clip(*in.Name, 60)
		if name == "" {
			return false, LedgerError("name is required")
		}
		sets, args = append(sets, "name = ?"), append(args, name)
	}
	if in.Last4 != nil {
		last4, err := lastDigits(*in.Last4, 4)
		if err != nil {
			return false, err
		}
		sets, args = append(sets, "last4 = ?"), append(args, last4)
	}
	if in.SenderPatterns != nil {
		sets, args = append(sets, "sender_patterns = ?"), append(args, py.JSONDumps(normalizePatterns(*in.SenderPatterns)))
	}
	if in.Forex != nil {
		sets, args = append(sets, "forex = ?"), append(args, boolInt(*in.Forex))
	}
	if len(sets) == 0 {
		found, err := GetAccount(ctx, q, id)
		return found != nil, err
	}
	res, err := q.ExecContext(ctx, fmt.Sprintf("UPDATE money_accounts SET %s WHERE id = ?", strings.Join(sets, ", ")), append(args, id)...)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return false, err
	}
	if in.Forex != nil {
		ids, err := queryIDs(ctx, q, "SELECT id FROM transactions WHERE account_id = ? AND kind != ?", id, FXAdjustment)
		if err != nil {
			return false, err
		}
		for _, txID := range ids {
			if err := rederive(ctx, q, txID); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func DeleteAccount(ctx context.Context, q db.Querier, id int64) (bool, error) {
	var used int64
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM transactions WHERE account_id = ?", id).Scan(&used); err != nil {
		return false, err
	}
	if used != 0 {
		return false, LedgerError(fmt.Sprintf("account has %d transactions; reassign them first", used))
	}
	res, err := q.ExecContext(ctx, "DELETE FROM money_accounts WHERE id = ?", id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func SenderMatches(a *Account, fromAddr, fromDomain string) bool {
	addr, domain := py.Lower(fromAddr), py.Lower(fromDomain)
	for _, p := range a.SenderPatterns {
		if strings.Contains(addr, p) || domain == p || strings.HasSuffix(domain, "."+p) {
			return true
		}
	}
	return false
}
