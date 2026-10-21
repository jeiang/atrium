package moneytools

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

func readTools() []tool.Tool {
	return build([]spec{
		{
			name: "spending",
			description: "Spending totals from the ledger for a period ('month' = this month, YYYY-MM, YYYY, YYYY-MM-DD/YYYY-MM-DD, or Nd for the last N days), " +
				"grouped by category, payee or account, per currency (TTD and BBD stay separate). Transfers and card payments are not spending.",
			schema:  `{"type":"object","properties":{"period":{"type":"string"},"group_by":{"type":"string","enum":["category","payee","account"]},"currency":{"type":"string","enum":["TTD","BBD"]}},"required":[],"additionalProperties":false}`,
			handler: spending, effect: tool.Read, kinds: readKinds,
		},
		{
			name: "transactions",
			description: "List ledger transactions, newest first. Filters: start/end (YYYY-MM-DD), account_id, payee (substring), category, kind (purchase, atm_withdrawal, " +
				"transfer, card_payment, refund, fx_adjustment), currency, unassigned (no account or flagged for review). Each has the original amount and the resolved account-currency amount.",
			schema: `{"type":"object","properties":{"start":{"type":"string"},"end":{"type":"string"},"account_id":{"type":"integer"},"payee":{"type":"string"},"category":{"type":"string"},` +
				`"kind":{"type":"string","enum":["purchase","atm_withdrawal","transfer","card_payment","refund","fx_adjustment"]},"currency":{"type":"string","enum":["TTD","BBD"]},` +
				`"unassigned":{"type":"boolean"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":[],"additionalProperties":false}`,
			handler: transactions, effect: tool.Read, kinds: readKinds,
		},
		{
			name: "bills",
			description: "Recurring bills and subscriptions seen in the ledger: payee, cadence, typical amount per currency, last charge and next expected date. " +
				"upcoming_days limits to bills expected within that many days from today.",
			schema:  `{"type":"object","properties":{"upcoming_days":{"type":"integer","minimum":0,"maximum":365}},"required":[],"additionalProperties":false}`,
			handler: bills, effect: tool.Read, kinds: readKinds,
		},
		{
			name:        "payees",
			description: "Search ledger payees by name: id, category, transaction count, bill override and aliases (the normalized descriptors that map to this payee).",
			schema:      `{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":[],"additionalProperties":false}`,
			handler:     payees, effect: tool.Read, kinds: readKinds,
		},
	})
}

func upper(s string) string { return py.Upper(s) }

func spending(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	period, _, err := tool.TextArg(args, "period", false)
	if err != nil {
		return nil, err
	}
	day, err := today(tc)
	if err != nil {
		return nil, err
	}
	start, end, err := money.PeriodBounds(period, day)
	if err != nil {
		return nil, raised(err)
	}
	groupBy, ok, err := tool.TextArg(args, "group_by", false)
	if err != nil {
		return nil, err
	}
	if !ok {
		groupBy = "category"
	}
	currency, _, err := tool.TextArg(args, "currency", false)
	if err != nil {
		return nil, err
	}
	found, err := money.Spending(ctx, tc.DB.Read, start, end, groupBy, upper(currency), day)
	if err != nil {
		return nil, raised(err)
	}
	codes := make([]string, 0, len(found))
	for code := range found {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	out := make(py.Dict, 0, len(codes))
	for _, code := range codes {
		bucket := found[code]
		groups := make([]any, len(bucket.Groups))
		for i, g := range bucket.Groups {
			groups[i] = py.Dict{{Key: "name", Value: g.Name}, {Key: "total", Value: g.Total}, {Key: "count", Value: int64(g.Count)}}
		}
		out = append(out, py.Pair{Key: code, Value: py.Dict{{Key: "total", Value: bucket.Total}, {Key: "groups", Value: groups}}})
	}
	return py.Dict{
		{Key: "from", Value: start}, {Key: "to", Value: end}, {Key: "group_by", Value: groupBy},
		{Key: "spending", Value: out}, {Key: "note", Value: note},
	}, nil
}

func transactions(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	r := assistant.Read(ctx, tc.DB.Read)
	maxLimit := r.Int(assistant.KeyMoneyMaxLimit)
	if err := r.Err(); err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 50, maxLimit, 1)
	if err != nil {
		return nil, err
	}
	var f money.Filter
	for _, field := range []struct {
		name string
		into *string
	}{{"start", &f.Start}, {"end", &f.End}, {"payee", &f.Payee}, {"category", &f.Category}, {"kind", &f.Kind}} {
		if *field.into, _, err = tool.TextArg(args, field.name, false); err != nil {
			return nil, err
		}
	}
	currency, _, err := tool.TextArg(args, "currency", false)
	if err != nil {
		return nil, err
	}
	f.Currency = upper(currency)
	if f.AccountID, _, err = intArg(args, "account_id", false); err != nil {
		return nil, err
	}
	unassigned := false
	if v, present := args.Get("unassigned"); present {
		b, isBool := v.(bool)
		if !isBool {
			return nil, tool.Error("unassigned must be true or false")
		}
		unassigned = b
	}
	day, err := today(tc)
	if err != nil {
		return nil, err
	}
	rows, err := money.ListTransactions(ctx, tc.DB.Read, f, unassigned, int(limit), false, day)
	if err != nil {
		return nil, raised(err)
	}
	out := make([]any, len(rows))
	for i, t := range rows {
		var review any
		if t.Review != "" {
			review = t.Review
		}
		out[i] = py.Dict{
			{Key: "id", Value: t.ID}, {Key: "date", Value: t.Date}, {Key: "kind", Value: t.Kind}, {Key: "payee", Value: t.DisplayPayee},
			{Key: "payee_id", Value: opt(t.PayeeID)}, {Key: "category", Value: opt(t.Category)}, {Key: "account", Value: opt(t.Account)},
			{Key: "account_id", Value: opt(t.AccountID)}, {Key: "amount", Value: opt(t.Amount)}, {Key: "currency", Value: opt(t.Currency)},
			{Key: "original_amount", Value: t.OriginalAmount}, {Key: "original_currency", Value: t.OriginalCurrency},
			{Key: "computed_amount", Value: opt(t.ComputedAmount)}, {Key: "description", Value: t.Description}, {Key: "review", Value: review},
			{Key: "sources", Value: t.SourceCount}, {Key: "message_id", Value: opt(t.MessageID)}, {Key: "transfer_id", Value: opt(t.TransferID)},
		}
	}
	return py.Dict{{Key: "transactions", Value: out}, {Key: "note", Value: note}}, nil
}

func bills(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	var days *int
	if v, _ := args.Get("upcoming_days"); v != nil {
		r := assistant.Read(ctx, tc.DB.Read)
		maxDays := r.Int(assistant.KeyMoneyMaxDays)
		if err := r.Err(); err != nil {
			return nil, err
		}
		n, err := tool.BoundedInt(args, "upcoming_days", 30, maxDays, 0)
		if err != nil {
			return nil, err
		}
		d := int(n)
		days = &d
	}
	day, err := today(tc)
	if err != nil {
		return nil, err
	}
	found, err := money.Bills(ctx, tc.DB.Read, day, days)
	if err != nil {
		return nil, raised(err)
	}
	out := make([]any, len(found))
	for i, b := range found {
		typical := make(py.Dict, 0, len(b.Currencies))
		for _, code := range b.Currencies {
			typical = append(typical, py.Pair{Key: code, Value: b.Typical[code]})
		}
		var cadence, daysUntil any
		if b.Cadence != "" {
			cadence = b.Cadence
		}
		if b.DaysUntil != nil {
			daysUntil = int64(*b.DaysUntil)
		}
		out[i] = py.Dict{
			{Key: "payee_id", Value: b.PayeeID}, {Key: "payee", Value: b.Payee}, {Key: "category", Value: opt(b.Category)},
			{Key: "cadence", Value: cadence}, {Key: "typical", Value: typical}, {Key: "last_charge", Value: b.LastCharge},
			{Key: "next_expected", Value: opt(b.NextExpected)}, {Key: "days_until", Value: daysUntil},
			{Key: "subscription_seen", Value: b.SubscriptionSeen}, {Key: "charges", Value: int64(b.Charges)},
		}
	}
	return py.Dict{
		{Key: "bills", Value: out},
		{Key: "note", Value: "A bill is a payee with subscription wording in its mail or regular similar charges; next_expected is computed from the last charge, not promised by anyone."},
	}, nil
}

func payees(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	query, _, err := tool.TextArg(args, "query", false)
	if err != nil {
		return nil, err
	}
	r := assistant.Read(ctx, tc.DB.Read)
	maxLimit := r.Int(assistant.KeyMoneyMaxLimit)
	if err := r.Err(); err != nil {
		return nil, err
	}
	limit, err := tool.BoundedInt(args, "limit", 50, maxLimit, 1)
	if err != nil {
		return nil, err
	}
	found, err := money.ListPayees(ctx, tc.DB.Read, query, int(limit))
	if err != nil {
		return nil, raised(err)
	}
	out := make([]any, len(found))
	for i, p := range found {
		aliases := make([]any, len(p.Aliases))
		for j, a := range p.Aliases {
			aliases[j] = py.Dict{{Key: "id", Value: a.ID}, {Key: "kind", Value: a.Kind}, {Key: "pattern", Value: a.Pattern}}
		}
		out[i] = py.Dict{
			{Key: "id", Value: p.ID}, {Key: "name", Value: p.Name}, {Key: "category", Value: opt(p.Category)},
			{Key: "transactions", Value: p.Transactions}, {Key: "last_date", Value: opt(p.LastDate)},
			{Key: "bill_override", Value: opt(p.RecurringOverride)}, {Key: "aliases", Value: aliases},
		}
	}
	return py.Dict{{Key: "payees", Value: out}}, nil
}
