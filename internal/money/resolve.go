package money

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/db"
)

type Resolved struct {
	Amount   *float64
	Rate     *float64
	RateDate *string
	Review   string
}

func Resolve(ctx context.Context, q db.Querier, account *Account, amount float64, currency, day string) (Resolved, error) {
	held := account.Currency
	if currency == held {
		return Resolved{Amount: roundPtr(&amount)}, nil
	}
	if !account.Forex || !contains(TxCurrencies, currency) {
		return Resolved{Review: fmt.Sprintf("%s charge on a %s account", currency, held)}, nil
	}
	if held == BBD && currency == USD {
		rate := bbdPerUSD * bbdForexFee
		converted := round2(amount * rate)
		return Resolved{Amount: &converted, Rate: &rate}, nil
	}
	found, err := rateOn(ctx, q, day)
	if err != nil {
		return Resolved{}, err
	}
	if found == nil {
		return Resolved{Review: "no CBTT rate stored yet"}, nil
	}
	var rate float64
	switch {
	case held != TTD:
		rate = bbdPerUSD * bbdForexFee / found.selling
	case currency == USD:
		rate = found.selling
	default:
		rate = found.selling / bbdPerUSD
	}
	converted := round2(amount * rate)
	date := found.date
	return Resolved{Amount: &converted, Rate: &rate, RateDate: &date}, nil
}
