package moneytools

import (
	"context"
	"math/big"
	"strconv"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

func writeTools() []tool.Tool {
	return build([]spec{
		{
			name:        "payee_rename",
			description: "Rename a ledger payee. Logged and undoable by the operator.",
			schema:      `{"type":"object","properties":{"payee_id":{"type":"integer"},"name":{"type":"string","maxLength":80}},"required":["payee_id","name"],"additionalProperties":false}`,
			handler:     payeeRename, effect: tool.Write, kinds: writeKinds,
		},
		{
			name:        "payee_merge",
			description: "Merge payee source_id into target_id: its aliases and transactions move to the target and the source is removed. Logged and undoable.",
			schema:      `{"type":"object","properties":{"source_id":{"type":"integer"},"target_id":{"type":"integer"}},"required":["source_id","target_id"],"additionalProperties":false}`,
			handler:     payeeMerge, effect: tool.Write, kinds: writeKinds,
		},
		{
			name: "payee_alias_add",
			description: "Add a descriptor alias to a payee so future mail with that descriptor maps to it. kind exact (default) or prefix match the normalized descriptor " +
				"(uppercase, no '*', store numbers or country); regex is a Python regex.",
			schema:  `{"type":"object","properties":{"payee_id":{"type":"integer"},"pattern":{"type":"string","maxLength":120},"kind":{"type":"string","enum":["exact","prefix","regex"]}},"required":["payee_id","pattern"],"additionalProperties":false}`,
			handler: payeeAliasAdd, effect: tool.Write, kinds: writeKinds,
		},
		{
			name:        "payee_set_category",
			description: "Set a payee's category by name (the store-to-category rule for its transactions). Logged and undoable.",
			schema:      `{"type":"object","properties":{"payee_id":{"type":"integer"},"category":{"type":"string"}},"required":["payee_id","category"],"additionalProperties":false}`,
			handler:     payeeSetCategory, effect: tool.Write, kinds: writeKinds,
		},
		{
			name: "transaction_merge",
			description: "Merge transactions that describe one purchase (for example a bank alert and the merchant's receipt) into one; at most one may come from a bank alert, " +
				"whose account and amount are kept. Logged and undoable.",
			schema:  `{"type":"object","properties":{"transaction_ids":{"type":"array","items":{"type":"integer"},"minItems":2}},"required":["transaction_ids"],"additionalProperties":false}`,
			handler: transactionMerge, effect: tool.Write, kinds: writeKinds,
		},
		{
			name:        "transaction_split",
			description: "Split a merged transaction back into one transaction per source mail. Logged and undoable.",
			schema:      `{"type":"object","properties":{"transaction_id":{"type":"integer"}},"required":["transaction_id"],"additionalProperties":false}`,
			handler:     transactionSplit, effect: tool.Write, kinds: writeKinds,
		},
		{
			name:        "transaction_set_account",
			description: "Assign a transaction to one of the operator's accounts (see /money/accounts); the amount is re-resolved in that account's currency. Logged and undoable.",
			schema:      `{"type":"object","properties":{"transaction_id":{"type":"integer"},"account_id":{"type":"integer"}},"required":["transaction_id","account_id"],"additionalProperties":false}`,
			handler:     transactionSetAccount, effect: tool.Write, kinds: writeKinds,
		},
	})
}

func target(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }

func payeeRename(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	payeeID, _, err := intArg(args, "payee_id", true)
	if err != nil {
		return nil, err
	}
	name, _, err := tool.TextArg(args, "name", true)
	if err != nil {
		return nil, err
	}
	return write(ctx, tc, "payee_rename", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		change, err := money.PayeeRename(ctx, q, payeeID, name)
		return target("payee", payeeID), change, py.Dict{{Key: "payee_id", Value: payeeID}, {Key: "name", Value: name}}, err
	})
}

func payeeMerge(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	source, _, err := intArg(args, "source_id", true)
	if err != nil {
		return nil, err
	}
	dest, _, err := intArg(args, "target_id", true)
	if err != nil {
		return nil, err
	}
	return write(ctx, tc, "payee_merge", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		change, err := money.PayeeMerge(ctx, q, source, dest)
		return target("payee", dest), change, py.Dict{{Key: "merged_into", Value: dest}, {Key: "removed", Value: source}}, err
	})
}

func payeeAliasAdd(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	payeeID, _, err := intArg(args, "payee_id", true)
	if err != nil {
		return nil, err
	}
	pattern, _, err := tool.TextArg(args, "pattern", true)
	if err != nil {
		return nil, err
	}
	kind, ok, err := tool.TextArg(args, "kind", false)
	if err != nil {
		return nil, err
	}
	if !ok {
		kind = money.Exact
	}
	return write(ctx, tc, "payee_alias_add", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		aliasID, change, err := money.PayeeAliasAdd(ctx, q, payeeID, pattern, kind)
		return target("payee", payeeID), change, py.Dict{{Key: "alias_id", Value: aliasID}, {Key: "payee_id", Value: payeeID}}, err
	})
}

func payeeSetCategory(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	payeeID, _, err := intArg(args, "payee_id", true)
	if err != nil {
		return nil, err
	}
	name, _, err := tool.TextArg(args, "category", true)
	if err != nil {
		return nil, err
	}
	return write(ctx, tc, "payee_set_category", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		none := target("payee", payeeID)
		catID, err := money.CategoryID(ctx, q, name)
		if err != nil {
			return none, nil, nil, err
		}
		if catID == nil {
			cats, err := money.Categories(ctx, q)
			if err != nil {
				return none, nil, nil, err
			}
			names := ""
			for i, c := range cats {
				if i > 0 {
					names += ", "
				}
				names += c.Name
			}
			return none, nil, nil, tool.Error("unknown category " + py.Repr(name) + "; categories are " + names)
		}
		change, err := money.PayeeSetCategory(ctx, q, payeeID, catID)
		return none, change, py.Dict{{Key: "payee_id", Value: payeeID}, {Key: "category", Value: name}}, err
	})
}

func transactionMerge(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	raw, _ := args.Get("transaction_ids")
	list, isList := raw.([]any)
	if !isList {
		return nil, tool.Error("transaction_ids must be a list of integers")
	}
	ids := make([]int64, len(list))
	for i, v := range list {
		switch n := v.(type) {
		case int64:
			ids[i] = n
		case *big.Int:
			return nil, &tool.Raised{Class: "OverflowError"}
		default:
			return nil, tool.Error("transaction_ids must be a list of integers")
		}
	}
	return write(ctx, tc, "transaction_merge", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		kept, change, err := money.TransactionMerge(ctx, q, ids)
		return target("transaction", kept), change, py.Dict{{Key: "transaction_id", Value: kept}}, err
	})
}

func transactionSplit(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	txID, _, err := intArg(args, "transaction_id", true)
	if err != nil {
		return nil, err
	}
	return write(ctx, tc, "transaction_split", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		made, change, err := money.TransactionSplit(ctx, q, txID, tc.Now())
		out := make([]any, len(made))
		for i, id := range made {
			out[i] = id
		}
		return target("transaction", txID), change, py.Dict{{Key: "transaction_ids", Value: out}}, err
	})
}

func transactionSetAccount(ctx context.Context, tc *tool.Context, args py.Dict) (any, error) {
	txID, _, err := intArg(args, "transaction_id", true)
	if err != nil {
		return nil, err
	}
	accountID, _, err := intArg(args, "account_id", true)
	if err != nil {
		return nil, err
	}
	return write(ctx, tc, "transaction_set_account", func(q db.Querier) (string, *money.Change, py.Dict, error) {
		at := target("transaction", txID)
		change, err := money.TransactionSetAccount(ctx, q, txID, accountID)
		if err != nil {
			return at, nil, nil, err
		}
		found, err := money.GetTransaction(ctx, q, txID)
		if err != nil {
			return at, nil, nil, err
		}
		if found == nil {
			return at, nil, nil, &tool.Raised{Class: "TypeError"}
		}
		var review any
		if found.Review != "" {
			review = found.Review
		}
		return at, change, py.Dict{
			{Key: "transaction_id", Value: txID}, {Key: "account", Value: opt(found.Account)}, {Key: "amount", Value: opt(found.Amount)},
			{Key: "currency", Value: opt(found.Currency)}, {Key: "review", Value: review},
		}, nil
	})
}
