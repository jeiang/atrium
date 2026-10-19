package money

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

func NormalizeRaw(raw string) (string, error) {
	text, err := storeNumberRE.Sub(" ", strings.ReplaceAll(py.Upper(raw), "*", " "), 0)
	if err != nil {
		return "", err
	}
	if text, err = nonDescriptorRE.Sub(" ", text, 0); err != nil {
		return "", err
	}
	tokens := py.Split(text)
	for len(tokens) > 0 && countryCodes[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	return strings.Join(tokens, " "), nil
}

func MatchAlias(ctx context.Context, q db.Querier, normalized string) (*int64, error) {
	if normalized == "" {
		return nil, nil
	}
	var id int64
	err := q.QueryRowContext(ctx, "SELECT payee_id FROM payee_aliases WHERE kind = ? AND pattern = ?", Exact, normalized).Scan(&id)
	if err == nil {
		return &id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	type alias struct {
		payeeID int64
		pattern string
	}
	load := func(kind string) ([]alias, error) {
		rows, err := q.QueryContext(ctx, "SELECT payee_id, pattern FROM payee_aliases WHERE kind = ?", kind)
		if err != nil {
			return nil, err
		}
		return db.Collect(rows, func(r *sql.Rows) (alias, error) {
			var a alias
			return a, r.Scan(&a.payeeID, &a.pattern)
		})
	}
	prefixes, err := load(Prefix)
	if err != nil {
		return nil, err
	}
	var best *alias
	for i, a := range prefixes {
		if strings.HasPrefix(normalized, a.pattern) && (best == nil || len(a.pattern) > len(best.pattern)) {
			best = &prefixes[i]
		}
	}
	if best != nil {
		return &best.payeeID, nil
	}
	regexes, err := load(Regex)
	if err != nil {
		return nil, err
	}
	for _, a := range regexes {
		re, err := rx.Compile(a.pattern, rx.IgnoreCase)
		if err != nil {
			continue
		}
		m, err := re.Search(normalized)
		if err != nil {
			return nil, err
		}
		if m != nil {
			id := a.payeeID
			return &id, nil
		}
	}
	return nil, nil
}

type Category struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func Categories(ctx context.Context, q db.Querier) ([]Category, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, name FROM categories ORDER BY name")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Category, error) {
		var c Category
		return c, r.Scan(&c.ID, &c.Name)
	})
}

func CategoryID(ctx context.Context, q db.Querier, name string) (*int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM categories WHERE name = ?", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func AddCategory(ctx context.Context, q db.Querier, name string) (int64, error) {
	name = clip(name, 40)
	if name == "" {
		return 0, LedgerError("category name is required")
	}
	res, err := q.ExecContext(ctx, "INSERT OR IGNORE INTO categories (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func RenameCategory(ctx context.Context, q db.Querier, id int64, name string) (bool, error) {
	name = clip(name, 40)
	if name == "" {
		return false, LedgerError("category name is required")
	}
	res, err := q.ExecContext(ctx, "UPDATE categories SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type Payee struct {
	ID                int64   `json:"id"`
	Name              string  `json:"name"`
	CategoryID        *int64  `json:"category_id"`
	RecurringOverride *string `json:"recurring_override"`
	Cadence           string  `json:"cadence"`
	SubscriptionSeen  int64   `json:"subscription_seen"`
	CreatedTS         float64 `json:"created_ts"`
	Category          *string `json:"category"`
}

const payeeColumns = "p.id, p.name, p.category_id, p.recurring_override, p.cadence, p.subscription_seen, p.created_ts, c.name"

func scanPayee(r interface{ Scan(...any) error }, extra ...any) (Payee, error) {
	var p Payee
	targets := append([]any{&p.ID, &p.Name, &p.CategoryID, &p.RecurringOverride, &p.Cadence, &p.SubscriptionSeen, &p.CreatedTS, &p.Category}, extra...)
	return p, r.Scan(targets...)
}

func GetPayee(ctx context.Context, q db.Querier, id int64) (*Payee, error) {
	p, err := scanPayee(q.QueryRowContext(ctx,
		"SELECT "+payeeColumns+" FROM payees p LEFT JOIN categories c ON c.id = p.category_id WHERE p.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func FindPayeeByName(ctx context.Context, q db.Querier, name string) (*int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, "SELECT id FROM payees WHERE lower(name) = lower(?)", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func CreatePayee(ctx context.Context, q db.Querier, name, category string, now float64, change *Change) (int64, error) {
	name = clip(name, 80)
	if name == "" {
		return 0, LedgerError("payee name is required")
	}
	var cat *int64
	if category != "" {
		var err error
		if cat, err = CategoryID(ctx, q, category); err != nil {
			return 0, err
		}
	}
	res, err := q.ExecContext(ctx, "INSERT INTO payees (name, category_id, created_ts) VALUES (?, ?, ?)", name, cat, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if change != nil {
		change.Created("payees", id)
	}
	return id, nil
}

func AddAlias(ctx context.Context, q db.Querier, payeeID int64, pattern, kind string, change *Change) (int64, error) {
	if !contains(AliasKinds, kind) {
		return 0, LedgerError("alias kind must be one of " + joinList(AliasKinds))
	}
	if kind != Regex {
		var err error
		if pattern, err = NormalizeRaw(pattern); err != nil {
			return 0, err
		}
	} else {
		pattern = py.Strip(pattern)
	}
	if pattern == "" {
		return 0, LedgerError("alias pattern is empty after normalization")
	}
	if kind == Regex {
		if _, err := rx.Compile(pattern, 0); err != nil {
			return 0, LedgerError("bad regex: " + err.Error())
		}
	}
	var existingID, existingPayee int64
	err := q.QueryRowContext(ctx, "SELECT id, payee_id FROM payee_aliases WHERE kind = ? AND pattern = ?", kind, pattern).Scan(&existingID, &existingPayee)
	if err == nil {
		if existingPayee != payeeID {
			if change != nil {
				if err := change.Touch(ctx, q, "payee_aliases", existingID); err != nil {
					return 0, err
				}
			}
			if _, err := q.ExecContext(ctx, "UPDATE payee_aliases SET payee_id = ? WHERE id = ?", payeeID, existingID); err != nil {
				return 0, err
			}
		}
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.ExecContext(ctx, "INSERT INTO payee_aliases (payee_id, pattern, kind) VALUES (?, ?, ?)", payeeID, pattern, kind)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if change != nil {
		change.Created("payee_aliases", id)
	}
	return id, nil
}
