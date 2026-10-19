package pages

import (
	"slices"

	"github.com/jeiang/atrium/internal/money"
	"github.com/jeiang/atrium/internal/py"
)

type txView struct {
	ID               int64    `json:"id"`
	Date             string   `json:"date"`
	DisplayPayee     string   `json:"display_payee"`
	Kind             string   `json:"kind"`
	Review           string   `json:"review"`
	Amount           *float64 `json:"amount"`
	Currency         *string  `json:"currency"`
	OriginalAmount   float64  `json:"original_amount"`
	OriginalCurrency string   `json:"original_currency"`
	ComputedAmount   *float64 `json:"computed_amount"`
	Account          *string  `json:"account"`
	AccountID        *int64   `json:"account_id"`
	Description      string   `json:"description"`
	MessageID        *int64   `json:"message_id"`
	Category         *string  `json:"category"`
	CategoryID       *int64   `json:"category_id"`
	SourceCount      int64    `json:"source_count"`
	TransferID       *int64   `json:"transfer_id"`
	Dismissed        string   `json:"dismissed"`
	IsTransfer       bool     `json:"is_transfer"`
}

type accountView struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Bank           string   `json:"bank"`
	BankLabel      string   `json:"bank_label"`
	Kind           string   `json:"kind"`
	Currency       string   `json:"currency"`
	Forex          int64    `json:"forex"`
	Last4          string   `json:"last4"`
	SenderPatterns []string `json:"sender_patterns"`
}

type categoryView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type aliasView struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

type payeeView struct {
	ID                int64       `json:"id"`
	Name              string      `json:"name"`
	Category          *string     `json:"category"`
	CategoryID        *int64      `json:"category_id"`
	RecurringOverride *string     `json:"recurring_override"`
	SubscriptionSeen  bool        `json:"subscription_seen"`
	Transactions      int64       `json:"transactions"`
	LastDate          *string     `json:"last_date"`
	Aliases           []aliasView `json:"aliases"`
}

type billView struct {
	PayeeID          int64   `json:"payee_id"`
	Payee            string  `json:"payee"`
	Cadence          string  `json:"cadence"`
	Category         *string `json:"category"`
	Override         *string `json:"override"`
	SubscriptionSeen bool    `json:"subscription_seen"`
	Typical          py.Dict `json:"typical"`
	LastCharge       string  `json:"last_charge"`
	NextExpected     *string `json:"next_expected"`
	DaysUntil        *int64  `json:"days_until"`
	Charges          int64   `json:"charges"`
}

type rateView struct {
	Date    string  `json:"date"`
	AsOf    string  `json:"as_of"`
	Selling float64 `json:"selling"`
}

type spendGroupView struct {
	Name  string  `json:"name"`
	Count int64   `json:"count"`
	Total float64 `json:"total"`
}

type spendBucketView struct {
	Total  float64          `json:"total"`
	Groups []spendGroupView `json:"groups"`
}

type filters struct {
	Start      string `json:"start"`
	End        string `json:"end"`
	AccountID  int64  `json:"account_id"`
	Payee      string `json:"payee"`
	Category   string `json:"category"`
	Kind       string `json:"kind"`
	Currency   string `json:"currency"`
	Unassigned bool   `json:"unassigned"`
	Dismissed  bool   `json:"dismissed"`
}

type overviewPage struct {
	Month      string        `json:"month"`
	Spending   py.Dict       `json:"spending"`
	Unassigned []txView      `json:"unassigned"`
	Accounts   []accountView `json:"accounts"`
	Bills      []billView    `json:"bills"`
	Rates      []rateView    `json:"rates"`
}

type transactionsPage struct {
	Transactions []txView       `json:"transactions"`
	Filters      filters        `json:"filters"`
	Accounts     []accountView  `json:"accounts"`
	Categories   []categoryView `json:"categories"`
	Kinds        []string       `json:"kinds"`
	Currencies   []string       `json:"currencies"`
}

type payeesPage struct {
	Payees     []payeeView    `json:"payees"`
	Query      string         `json:"query"`
	Categories []categoryView `json:"categories"`
	AliasKinds []string       `json:"alias_kinds"`
}

type billsPage struct {
	Bills []billView `json:"bills"`
}

type accountsPage struct {
	Accounts       []accountView `json:"accounts"`
	Banks          py.Dict       `json:"banks"`
	Kinds          []string      `json:"kinds"`
	Currencies     []string      `json:"currencies"`
	DefaultSenders py.Dict       `json:"default_senders"`
	Rates          []rateView    `json:"rates"`
	Since          *string       `json:"since"`
}

var pageKinds = []string{money.Purchase, money.ATM, money.Refund, money.Transfer, money.CardPayment, money.FXAdjustment}

func txViews(rows []money.ListedTransaction) []txView {
	out := make([]txView, len(rows))
	for i, r := range rows {
		out[i] = txView{
			ID: r.ID, Date: r.Date, DisplayPayee: r.DisplayPayee, Kind: r.Kind, Review: r.Review, Amount: r.Amount,
			Currency: r.Currency, OriginalAmount: r.OriginalAmount, OriginalCurrency: r.OriginalCurrency,
			ComputedAmount: r.ComputedAmount, Account: r.Account, AccountID: r.AccountID, Description: r.Description,
			MessageID: r.MessageID, Category: r.Category, CategoryID: r.CategoryID, SourceCount: r.SourceCount,
			TransferID: r.TransferID, Dismissed: r.Dismissed, IsTransfer: r.IsTransfer,
		}
	}
	return out
}

func accountViews(rows []money.Account) []accountView {
	out := make([]accountView, len(rows))
	for i, a := range rows {
		var forex int64
		if a.Forex {
			forex = 1
		}
		senders := a.SenderPatterns
		if senders == nil {
			senders = []string{}
		}
		out[i] = accountView{
			ID: a.ID, Name: a.Name, Bank: a.Bank, BankLabel: a.BankLabel, Kind: a.Kind, Currency: a.Currency,
			Forex: forex, Last4: a.Last4, SenderPatterns: senders,
		}
	}
	return out
}

func categoryViews(rows []money.Category) []categoryView {
	out := make([]categoryView, len(rows))
	for i, c := range rows {
		out[i] = categoryView{ID: c.ID, Name: c.Name}
	}
	return out
}

func payeeViews(rows []money.PayeeListing) []payeeView {
	out := make([]payeeView, len(rows))
	for i, p := range rows {
		aliases := make([]aliasView, len(p.Aliases))
		for j, a := range p.Aliases {
			aliases[j] = aliasView{ID: a.ID, Kind: a.Kind, Pattern: a.Pattern}
		}
		out[i] = payeeView{
			ID: p.ID, Name: p.Name, Category: p.Category, CategoryID: p.CategoryID, RecurringOverride: p.RecurringOverride,
			SubscriptionSeen: p.SubscriptionSeen != 0, Transactions: p.Transactions, LastDate: p.LastDate, Aliases: aliases,
		}
	}
	return out
}

func billViews(rows []money.Bill) []billView {
	out := make([]billView, len(rows))
	for i, b := range rows {
		typical := make(py.Dict, 0, len(b.Currencies))
		for _, c := range b.Currencies {
			typical = append(typical, py.Pair{Key: c, Value: b.Typical[c]})
		}
		var days *int64
		if b.DaysUntil != nil {
			d := int64(*b.DaysUntil)
			days = &d
		}
		out[i] = billView{
			PayeeID: b.PayeeID, Payee: b.Payee, Cadence: b.Cadence, Category: b.Category, Override: b.Override,
			SubscriptionSeen: b.SubscriptionSeen, Typical: typical, LastCharge: b.LastCharge, NextExpected: b.NextExpected,
			DaysUntil: days, Charges: int64(b.Charges),
		}
	}
	return out
}

func rateViews(rows []money.StoredRate) []rateView {
	out := make([]rateView, len(rows))
	for i, r := range rows {
		out[i] = rateView{Date: r.Date, AsOf: r.AsOf, Selling: r.Selling}
	}
	return out
}

func spendingDict(spend map[string]money.SpendBucket) py.Dict {
	codes := make([]string, 0, len(spend))
	for code := range spend {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	out := make(py.Dict, 0, len(codes))
	for _, code := range codes {
		b := spend[code]
		groups := make([]spendGroupView, len(b.Groups))
		for i, g := range b.Groups {
			groups[i] = spendGroupView{Name: g.Name, Count: int64(g.Count), Total: g.Total}
		}
		out = append(out, py.Pair{Key: code, Value: spendBucketView{Total: b.Total, Groups: groups}})
	}
	return out
}

func bankDicts() (banks, senders py.Dict) {
	banks = make(py.Dict, 0, len(money.Banks))
	senders = make(py.Dict, 0, len(money.Banks))
	for _, b := range money.Banks {
		banks = append(banks, py.Pair{Key: b, Value: money.BankLabels[b]})
		senders = append(senders, py.Pair{Key: b, Value: money.DefaultSenders[b]})
	}
	return banks, senders
}
