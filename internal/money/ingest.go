package money

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/router"
	"github.com/jeiang/atrium/internal/py"
)

const Day = 86400.0

type Naming func(raw, normalized string, categories []string) (name, category string, err error)

func SuggestPayee(ctx context.Context, chat Chat, url, raw, normalized string, categories []string) (string, string, error) {
	fallbackName := titleCase(normalized)
	if fallbackName == "" {
		fallbackName = clip(raw, 80)
	}
	answer, err := chat(ctx, url, PayeeSystem, "Descriptor: "+raw+"\nNormalized: "+normalized, PayeeSchema(categories), 60)
	if err != nil {
		if _, down := unavailable(err); !down && !unreadable(err) {
			return "", "", err
		}
		log.Warn(fmt.Sprintf("payee suggestion failed for %s: %s", py.Repr(raw), httpx.Message(err)))
		return fallbackName, Other, nil
	}
	var name, category any
	if reply, ok := answer.(py.Dict); ok {
		name, _ = reply.Get("name")
		category, _ = reply.Get("category")
	}
	if label := clip(pyStr(name), 80); label != "" {
		fallbackName = label
	}
	if text, ok := category.(string); ok && contains(categories, text) {
		return fallbackName, text, nil
	}
	return fallbackName, Other, nil
}

func resolvePayee(ctx context.Context, q db.Querier, raw string, naming Naming, now float64, change *Change) (*int64, error) {
	normalized, err := NormalizeRaw(raw)
	if err != nil || normalized == "" {
		return nil, err
	}
	found, err := MatchAlias(ctx, q, normalized)
	if err != nil || found != nil {
		return found, err
	}
	categories, err := Categories(ctx, q)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = c.Name
	}
	name, category, err := naming(raw, normalized, names)
	if err != nil {
		return nil, err
	}
	payeeID, err := FindPayeeByName(ctx, q, name)
	if err != nil {
		return nil, err
	}
	if payeeID == nil {
		id, err := CreatePayee(ctx, q, name, category, now, change)
		if err != nil {
			return nil, err
		}
		payeeID = &id
	}
	if _, err := AddAlias(ctx, q, *payeeID, normalized, Exact, change); err != nil {
		return nil, err
	}
	return payeeID, nil
}

func loadTransaction(ctx context.Context, q db.Querier, id int64) (*TransactionRow, error) {
	var t TransactionRow
	err := q.QueryRowContext(ctx, "SELECT "+transactionColumns+" FROM transactions t WHERE t.id = ?", id).Scan(t.targets()...)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (m Merge) near(a, b *float64) bool {
	if a == nil || b == nil {
		return false
	}
	return math.Abs(*a-*b) <= m.Tolerance*max(math.Abs(*a), math.Abs(*b), 0.01)
}

func amountIn(ctx context.Context, q db.Querier, account *Account, tx *TransactionRow) (*float64, error) {
	if account == nil {
		return nil, nil
	}
	if tx.AccountID != nil && *tx.AccountID == account.ID && tx.Amount != nil {
		return tx.Amount, nil
	}
	resolved, err := Resolve(ctx, q, account, tx.OriginalAmount, tx.OriginalCurrency, tx.Date)
	return resolved.Amount, err
}

func roleSet(sources []Source) map[string]bool {
	out := map[string]bool{}
	for _, s := range sources {
		out[s.Role] = true
	}
	return out
}

func FindMergeTarget(ctx context.Context, q db.Querier, tx *TransactionRow) (*TransactionRow, error) {
	if tx.PayeeID == nil || (tx.Kind != Purchase && tx.Kind != Refund) {
		return nil, nil
	}
	merge, err := LoadMerge(ctx, q)
	if err != nil {
		return nil, err
	}
	own, err := sourcesOf(ctx, q, tx.ID)
	if err != nil {
		return nil, err
	}
	roles := roleSet(own)
	rows, err := q.QueryContext(ctx,
		"SELECT "+transactionColumns+" FROM transactions t WHERE t.id != ? AND t.payee_id = ? AND t.kind = ? AND "+
			"abs(julianday(t.date) - julianday(?)) <= ? ORDER BY t.id", tx.ID, *tx.PayeeID, tx.Kind, tx.Date, merge.Days)
	if err != nil {
		return nil, err
	}
	candidates, err := db.Collect(rows, func(r *sql.Rows) (TransactionRow, error) {
		var t TransactionRow
		return t, r.Scan(t.targets()...)
	})
	if err != nil {
		return nil, err
	}
	for i := range candidates {
		other := &candidates[i]
		theirs, err := sourcesOf(ctx, q, other.ID)
		if err != nil {
			return nil, err
		}
		shared := false
		for role := range roleSet(theirs) {
			shared = shared || roles[role]
		}
		if shared {
			continue
		}
		accountID := other.AccountID
		if !truthyID(accountID) {
			accountID = tx.AccountID
		}
		var account *Account
		if truthyID(accountID) {
			if account, err = GetAccount(ctx, q, *accountID); err != nil {
				return nil, err
			}
		}
		if account == nil {
			if other.OriginalCurrency == tx.OriginalCurrency && merge.near(&other.OriginalAmount, &tx.OriginalAmount) {
				return other, nil
			}
			continue
		}
		theirAmount, err := amountIn(ctx, q, account, other)
		if err != nil {
			return nil, err
		}
		ourAmount, err := amountIn(ctx, q, account, tx)
		if err != nil {
			return nil, err
		}
		if merge.near(theirAmount, ourAmount) {
			return other, nil
		}
	}
	return nil, nil
}

func LinkTransfer(ctx context.Context, q db.Querier, tx *TransactionRow, change *Change) (*int64, error) {
	if tx.AccountID == nil || tx.Amount == nil {
		return nil, nil
	}
	merge, err := LoadMerge(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx,
		"SELECT t.id, t.amount FROM transactions t WHERE t.id != ? AND t.kind IN (?, ?) AND t.transfer_id IS NULL AND "+
			"t.account_id IS NOT NULL AND t.account_id != ? AND t.currency = ? AND abs(julianday(t.date) - julianday(?)) <= ? ORDER BY t.id",
		tx.ID, Transfer, CardPayment, *tx.AccountID, tx.Currency, tx.Date, merge.Days)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		id     int64
		amount *float64
	}
	candidates, err := db.Collect(rows, func(r *sql.Rows) (candidate, error) {
		var c candidate
		return c, r.Scan(&c.id, &c.amount)
	})
	if err != nil {
		return nil, err
	}
	for _, c := range candidates {
		if !merge.near(c.amount, tx.Amount) {
			continue
		}
		if err := change.Touch(ctx, q, "transactions", c.id); err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, "UPDATE transactions SET transfer_id = ? WHERE id = ?", tx.ID, c.id); err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, "UPDATE transactions SET transfer_id = ? WHERE id = ?", c.id, tx.ID); err != nil {
			return nil, err
		}
		return &c.id, nil
	}
	return nil, nil
}

func PostFXAdjustment(ctx context.Context, q db.Querier, account *Account, item Item, messageID int64, now float64, change *Change) (*int64, error) {
	if account == nil || account.Bank != Republic || account.Kind != CreditCard || item.StatementTotal == nil {
		return nil, nil
	}
	var one int
	err := q.QueryRowContext(ctx, "SELECT 1 FROM transaction_sources WHERE message_id = ? AND role = ?", messageID, StatementRole).Scan(&one)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var previous sql.NullString
	if err := q.QueryRowContext(ctx, "SELECT MAX(date) FROM transactions WHERE account_id = ? AND kind = ? AND date < ?",
		account.ID, FXAdjustment, item.Date).Scan(&previous); err != nil {
		return nil, err
	}
	start := previous.String
	if start == "" {
		cycleEnd, ok := ParseDate(item.Date)
		if !ok {
			return nil, fmt.Errorf("%w: Invalid isoformat string: %s", httpx.ErrValue, py.Repr(item.Date))
		}
		start = isoDate(addDays(cycleEnd, -31))
	}
	var charged float64
	if err := q.QueryRowContext(ctx,
		"SELECT COALESCE(SUM(CASE WHEN kind = ? THEN -amount ELSE amount END), 0) FROM transactions "+
			"WHERE account_id = ? AND kind IN (?, ?, ?) AND amount IS NOT NULL AND date > ? AND date <= ?",
		Refund, account.ID, Purchase, ATM, Refund, start, item.Date).Scan(&charged); err != nil {
		return nil, err
	}
	adjustment := round2(*item.StatementTotal - charged)
	if adjustment == 0 {
		return nil, nil
	}
	payee := item.PayeeRaw
	if payee == "" {
		payee = "Statement"
	}
	currency := account.Currency
	accountID := account.ID
	fields := Fields{
		AccountID: &accountID, Kind: FXAdjustment, Date: item.Date, PayeeRaw: payee,
		Description:    fmt.Sprintf("statement %.2f %s minus %.2f charged since %s", *item.StatementTotal, account.Currency, charged, start),
		OriginalAmount: adjustment, OriginalCurrency: account.Currency, Amount: &adjustment, Currency: &currency,
	}
	txID, err := insertTransaction(ctx, q, fields, nil, FXCategory, now, change)
	if err != nil {
		return nil, err
	}
	source := item
	source.AccountID = &accountID
	if _, err := addSource(ctx, q, txID, messageID, StatementRole, source.Dict(), now, change); err != nil {
		return nil, err
	}
	return &txID, nil
}

func PostItem(ctx context.Context, q db.Querier, item Item, row Message, naming Naming, now float64, change *Change) (*int64, error) {
	accounts, err := ListAccounts(ctx, q)
	if err != nil {
		return nil, err
	}
	account, role := MatchAccount(accounts, row, item)
	if item.Kind == Statement {
		return PostFXAdjustment(ctx, q, account, item, row.ID, now, change)
	}
	var payeeID *int64
	var category string
	switch {
	case item.Kind == Purchase || item.Kind == Refund:
		if payeeID, err = resolvePayee(ctx, q, item.PayeeRaw, naming, now, change); err != nil {
			return nil, err
		}
		if truthyID(payeeID) && (item.Subscription || item.Cadence != Unknown) {
			if err := change.Touch(ctx, q, "payees", *payeeID); err != nil {
				return nil, err
			}
			if _, err := q.ExecContext(ctx,
				"UPDATE payees SET subscription_seen = MAX(subscription_seen, ?), cadence = CASE WHEN ? != 'unknown' THEN ? ELSE cadence END WHERE id = ?",
				boolInt(item.Subscription), item.Cadence, item.Cadence, *payeeID); err != nil {
				return nil, err
			}
		}
	case item.Kind == ATM:
		category = Cash
	case contains(TransferKinds, item.Kind):
		category = TransferCategory
	}
	source := item
	if account != nil {
		id := account.ID
		source.AccountID = &id
	}
	fields, err := Derive(ctx, q, []Source{{Role: role, Item: source}})
	if err != nil {
		return nil, err
	}
	txID, err := insertTransaction(ctx, q, fields, payeeID, category, now, change)
	if err != nil {
		return nil, err
	}
	if _, err := addSource(ctx, q, txID, row.ID, role, source.Dict(), now, change); err != nil {
		return nil, err
	}
	tx, err := loadTransaction(ctx, q, txID)
	if err != nil {
		return nil, err
	}
	target, err := FindMergeTarget(ctx, q, tx)
	if err != nil {
		return nil, err
	}
	switch {
	case target != nil:
		if err := mergeInto(ctx, q, target.ID, txID, change); err != nil {
			return nil, err
		}
		txID = target.ID
	case contains(TransferKinds, tx.Kind):
		if _, err := LinkTransfer(ctx, q, tx, change); err != nil {
			return nil, err
		}
		if item.Kind == CardPayment {
			if _, err := PostFXAdjustment(ctx, q, account, item, row.ID, now, change); err != nil {
				return nil, err
			}
		}
	}
	return &txID, nil
}

type MessageSource interface {
	Unscanned(ctx context.Context, q db.Querier, now float64, windowDays, limit int) ([]Message, error)
	Body(ctx context.Context, q db.Querier, id int64) (string, error)
}

type MailSource struct{}

func (MailSource) Unscanned(ctx context.Context, q db.Querier, now float64, windowDays, limit int) ([]Message, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT m.id, m.date_ts, m.subject, m.from_name, m.from_addr, m.from_domain FROM messages m WHERE m.gone_at IS NULL AND m.sent = 0 AND "+
			"m.date_ts >= ? AND (m.has_money = 1 OR m.k_statement = 1 OR m.k_receipt = 1 OR m.k_order = 1) AND "+
			"NOT EXISTS (SELECT 1 FROM messages o JOIN money_scans s ON s.message_id = o.id "+
			"WHERE o.account = m.account AND o.digest = m.digest) ORDER BY m.date_ts ASC LIMIT ?",
		now-float64(windowDays)*Day, limit)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Message, error) {
		var m Message
		return m, r.Scan(&m.ID, &m.DateTS, &m.Subject, &m.FromName, &m.FromAddr, &m.FromDomain)
	})
}

func (MailSource) Body(ctx context.Context, q db.Querier, id int64) (string, error) {
	return router.MessageBody(ctx, q, id)
}

type Options struct {
	Chat   Chat
	Fetch  Fetch
	Source MessageSource
	Env    config.Env
}

func (o Options) chat() Chat {
	if o.Chat != nil {
		return o.Chat
	}
	return ChatJSON
}

func (o Options) source() MessageSource {
	if o.Source != nil {
		return o.Source
	}
	return MailSource{}
}

func (o Options) location() (*time.Location, error) {
	if o.Env != nil {
		return config.Location(o.Env)
	}
	return config.Location(config.OS)
}

type ScanResult struct{ Scanned, Posted int }

func markScanned(ctx context.Context, q db.Querier, messageID int64, found bool, now float64) error {
	_, err := q.ExecContext(ctx, "INSERT OR REPLACE INTO money_scans (message_id, ts, found) VALUES (?, ?, ?)", messageID, now, boolInt(found))
	return err
}

func (o Options) namingFor(ctx context.Context, d *db.DB, cfg config.Config, item Item) Naming {
	live := func(raw, normalized string, names []string) (string, string, error) {
		return SuggestPayee(ctx, o.chat(), cfg.ChatURL, raw, normalized, names)
	}
	if item.Kind != Purchase && item.Kind != Refund {
		return live
	}
	normalized, err := NormalizeRaw(item.PayeeRaw)
	if err != nil || normalized == "" {
		return live
	}
	if found, err := MatchAlias(ctx, d.Read, normalized); err != nil || found != nil {
		return live
	}
	categories, err := Categories(ctx, d.Read)
	if err != nil {
		return live
	}
	names := make([]string, len(categories))
	for i, c := range categories {
		names[i] = c.Name
	}
	name, category, err := live(item.PayeeRaw, normalized, names)
	return func(raw, norm string, current []string) (string, string, error) {
		if err != nil || raw != item.PayeeRaw || norm != normalized {
			return live(raw, norm, current)
		}
		return name, category, nil
	}
}

func Scan(ctx context.Context, d *db.DB, cfg config.Config, limit int, now float64, o Options) (ScanResult, error) {
	loc, err := o.location()
	if err != nil {
		return ScanResult{}, err
	}
	scanning, err := LoadScanning(ctx, d.Read)
	if err != nil {
		return ScanResult{}, err
	}
	banks, err := LoadBanks(ctx, d.Read)
	if err != nil {
		return ScanResult{}, err
	}
	bankIDs := BankIDs(banks)
	schema := ExtractSchema(bankIDs)
	source := o.source()
	rows, err := source.Unscanned(ctx, d.Read, now, scanning.BackfillDays, limit)
	if err != nil {
		return ScanResult{}, err
	}
	mc := Context{Now: func() float64 { return now }}
	var result ScanResult
scan:
	for _, row := range rows {
		body, err := source.Body(ctx, d.Read, row.ID)
		if err != nil {
			return result, err
		}
		text := fmt.Sprintf("From: %s <%s>\nSubject: %s\nBody:\n%s", row.FromName, row.FromAddr, row.Subject, py.Clip(body, scanning.BodyChars))
		answer, err := o.chat()(ctx, cfg.ChatURL, ExtractSystem, text, schema, 400)
		switch down, isDown := unavailable(err); {
		case isDown:
			log.Warn(fmt.Sprintf("chat unavailable, deferring money scan: %s", down))
			break scan
		case err != nil && unreadable(err):
			log.Warn(fmt.Sprintf("message %d: unreadable money answer: %s", row.ID, httpx.Message(err)))
			if err := markScanned(ctx, d.Write, row.ID, false, now); err != nil {
				return result, err
			}
			continue
		case err != nil:
			return result, err
		}
		item, err := CleanExtraction(answer, bankIDs, isoDate(LocalDate(row.DateTS, loc)))
		if err != nil {
			return result, err
		}
		result.Scanned++
		var txID *int64
		if item != nil {
			change := NewChange()
			naming := o.namingFor(ctx, d, cfg, *item)
			postErr := d.WithTx(ctx, func(tx *sql.Tx) error {
				var err error
				txID, err = PostItem(ctx, tx, *item, row, naming, now, change)
				return err
			})
			if postErr != nil {
				if err := ctx.Err(); err != nil {
					return result, err
				}
				log.Error(fmt.Sprintf("message %d: posting failed", row.ID), logx.Err, postErr.Error())
				txID = nil
			} else {
				target := fmt.Sprintf("message:%d", row.ID)
				if txID != nil {
					target = fmt.Sprintf("transaction:%d", *txID)
				}
				if _, err := change.Record(ctx, d.Write, mc, "money_post", target); err != nil {
					return result, err
				}
				if txID != nil {
					result.Posted++
				}
			}
		}
		if err := markScanned(ctx, d.Write, row.ID, txID != nil, now); err != nil {
			return result, err
		}
	}
	return result, nil
}

func EnsureRates(ctx context.Context, d *db.DB, now float64, o Options) (*FetchedRate, error) {
	var one int
	err := d.Read.QueryRowContext(ctx, "SELECT 1 FROM fx_rates LIMIT 1").Scan(&one)
	if err == nil {
		return nil, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	loc, err := o.location()
	if err != nil {
		return nil, err
	}
	got, err := FetchRates(ctx, d.Write, now, loc, o.Fetch)
	if err != nil {
		if _, down := unavailable(err); down || errors.Is(err, httpx.ErrValue) {
			log.Warn(fmt.Sprintf("CBTT rate fetch failed: %s", httpx.Message(err)))
			return nil, nil
		}
		return nil, err
	}
	return got, nil
}

func (r *FetchedRate) Dict() any {
	if r == nil {
		return nil
	}
	return py.Dict{{Key: "date", Value: r.Date}, {Key: "selling", Value: r.Selling}, {Key: "as_of", Value: r.AsOf}}
}

func Refresh(ctx context.Context, d *db.DB, cfg config.Config, limit int, now float64, o Options) (py.Dict, error) {
	rates, err := EnsureRates(ctx, d, now, o)
	if err != nil {
		return nil, err
	}
	out := py.Dict{{Key: "rates", Value: rates.Dict()}}
	result, err := Scan(ctx, d, cfg, limit, now, o)
	if err != nil {
		log.Error("money scan failed", logx.Err, err.Error())
		return append(out, py.Pair{Key: "error", Value: true}), nil
	}
	return append(out, py.Pair{Key: "scanned", Value: result.Scanned}, py.Pair{Key: "posted", Value: result.Posted}), nil
}
