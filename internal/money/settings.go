package money

import (
	"context"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	BanksKey            = "money_banks"
	BankSendersKey      = "money_bank_senders"
	CurrenciesKey       = "money_currencies"
	TxCurrenciesKey     = "money_tx_currencies"
	BBDPerUSDKey        = "money_bbd_per_usd"
	BBDForexFeeKey      = "money_bbd_forex_fee"
	CBTTURLKey          = "money_cbtt_url"
	WeeklyDaysKey       = "money_bill_weekly_days"
	MonthlyDaysKey      = "money_bill_monthly_days"
	YearlyDaysKey       = "money_bill_yearly_days"
	WeeklySlackKey      = "money_bill_weekly_slack"
	MonthlySlackKey     = "money_bill_monthly_slack"
	YearlySlackKey      = "money_bill_yearly_slack"
	BillToleranceKey    = "money_bill_amount_tolerance"
	MergeToleranceKey   = "money_merge_tolerance"
	MergeDaysKey        = "money_merge_days"
	BackfillDaysKey     = "money_backfill_days"
	ScanLimitKey        = "money_scan_limit"
	CLIScanLimitKey     = "money_cli_scan_limit"
	BodyCharsKey        = "money_body_chars"
	UnassignedLimitKey  = "money_unassigned_limit"
	TransactionLimitKey = "money_transaction_limit"
	BillsSoonDaysKey    = "money_bills_soon_days"
	defaultBanks        = "first_citizens=First Citizens;scotiabank=Scotiabank;republic=Republic;cibc=CIBC"
	defaultBankSenders  = "first_citizens=firstcitizenstt.com;scotiabank=scotiabank.com;republic=republictt.com,rfhl.com;cibc=cibcfcib.com,cibc.com"
	defaultCurrencies   = "TTD,BBD"
	defaultTxCurrencies = "TTD,BBD,USD"
	moduleName          = "money"
	maxCadenceDays      = 3660
	maxCadenceSlack     = 366
	maxScanLimit        = 100000
	maxBodyChars        = 100000
	maxPageRows         = 100000
	maxBackfillDays     = 3650
	maxMergeDays        = 30
)

type Bank struct {
	ID      string
	Label   string
	Senders []string
}

func init() {
	instance := []settings.Scope{settings.Instance}
	advanced := func(key string, typ settings.Type, def string, check func(string) error) settings.Def {
		return settings.Def{Key: key, Module: moduleName, Type: typ, Default: def, Check: check, Class: settings.Advanced, Scopes: instance}
	}
	settings.MustRegister(
		advanced(BanksKey, settings.String, defaultBanks, checkBanks),
		advanced(BankSendersKey, settings.String, defaultBankSenders, checkBankSenders),
		advanced(CurrenciesKey, settings.String, defaultCurrencies, checkCurrencies),
		advanced(TxCurrenciesKey, settings.String, defaultTxCurrencies, checkCurrencies),
		advanced(BBDPerUSDKey, settings.Float, "2.0", settings.Above(0)),
		advanced(BBDForexFeeKey, settings.Float, "1.02", settings.Between(1, 2)),
		settings.Def{Key: CBTTURLKey, Module: moduleName, Type: settings.String, Default: "https://www.central-bank.org.tt/", Check: checkHTTPURL, Class: settings.Bootstrap},
		advanced(WeeklyDaysKey, settings.Int, "7", settings.Between(1, maxCadenceDays)),
		advanced(MonthlyDaysKey, settings.Int, "30", settings.Between(1, maxCadenceDays)),
		advanced(YearlyDaysKey, settings.Int, "365", settings.Between(1, maxCadenceDays)),
		advanced(WeeklySlackKey, settings.Int, "2", settings.Between(0, maxCadenceSlack)),
		advanced(MonthlySlackKey, settings.Int, "5", settings.Between(0, maxCadenceSlack)),
		advanced(YearlySlackKey, settings.Int, "20", settings.Between(0, maxCadenceSlack)),
		advanced(BillToleranceKey, settings.Float, "0.15", settings.Within(0, 1)),
		advanced(MergeToleranceKey, settings.Float, "0.03", settings.Within(0, 1)),
		advanced(MergeDaysKey, settings.Int, "3", settings.Between(0, maxMergeDays)),
		advanced(BackfillDaysKey, settings.Int, "90", settings.Between(1, maxBackfillDays)),
		advanced(ScanLimitKey, settings.Int, "50", settings.Between(1, maxScanLimit)),
		advanced(CLIScanLimitKey, settings.Int, "1000", settings.Between(1, maxScanLimit)),
		advanced(BodyCharsKey, settings.Int, "3000", settings.Between(100, maxBodyChars)),
		advanced(UnassignedLimitKey, settings.Int, "50", settings.Between(1, maxPageRows)),
		advanced(TransactionLimitKey, settings.Int, "200", settings.Between(1, maxPageRows)),
		advanced(BillsSoonDaysKey, settings.Int, "14", settings.Between(1, maxBackfillDays)),
	)
}

func checkHTTPURL(value string) error {
	if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		return settings.Invalid("must be an http or https URL")
	}
	return nil
}

func snake(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, c := range s {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func entries(value string) ([][2]string, error) {
	var out [][2]string
	seen := map[string]bool{}
	for _, part := range strings.Split(value, ";") {
		id, rest, ok := strings.Cut(part, "=")
		id, rest = strings.TrimSpace(id), strings.TrimSpace(rest)
		if !ok || !snake(id) || rest == "" {
			return nil, settings.Invalid(fmt.Sprintf("entries must look like bank_id=value separated by semicolons, got %q", part))
		}
		if seen[id] {
			return nil, settings.Invalid(fmt.Sprintf("bank %q appears twice", id))
		}
		seen[id] = true
		out = append(out, [2]string{id, rest})
	}
	return out, nil
}

func checkBanks(value string) error {
	_, err := entries(value)
	return err
}

func domains(list string) ([]string, error) {
	var out []string
	for _, d := range strings.Split(list, ",") {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || strings.ContainsAny(d, " \t=;@") {
			return nil, settings.Invalid(fmt.Sprintf("invalid sender domain %q", d))
		}
		out = append(out, d)
	}
	return out, nil
}

func checkBankSenders(value string) error {
	found, err := entries(value)
	if err != nil {
		return err
	}
	for _, e := range found {
		if _, err := domains(e[1]); err != nil {
			return err
		}
	}
	return nil
}

func codes(value string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Split(value, ",") {
		c = strings.TrimSpace(c)
		if c != TTD && c != BBD && c != USD {
			return nil, settings.Invalid(fmt.Sprintf("currency must be one of %s, %s, %s, got %q", TTD, BBD, USD, c))
		}
		if seen[c] {
			return nil, settings.Invalid(fmt.Sprintf("currency %q appears twice", c))
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

func checkCurrencies(value string) error {
	_, err := codes(value)
	return err
}

type reader struct {
	ctx context.Context
	q   db.Querier
	err error
}

func (r *reader) text(key string) string {
	if r.err != nil {
		return ""
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	r.err = err
	return found.Value
}

func (r *reader) integer(key string) int {
	if r.err != nil {
		return 0
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	if err != nil {
		r.err = err
		return 0
	}
	n, err := found.Int()
	r.err = err
	return int(n)
}

func (r *reader) float(key string) float64 {
	if r.err != nil {
		return 0
	}
	found, err := settings.Default.Resolve(r.ctx, r.q, key, 0)
	if err != nil {
		r.err = err
		return 0
	}
	f, err := found.Float()
	r.err = err
	return f
}

func LoadBanks(ctx context.Context, q db.Querier) ([]Bank, error) {
	r := &reader{ctx: ctx, q: q}
	names, senders := r.text(BanksKey), r.text(BankSendersKey)
	if r.err != nil {
		return nil, r.err
	}
	labels, err := entries(names)
	if err != nil {
		return nil, err
	}
	byBank := map[string][]string{}
	listed, err := entries(senders)
	if err != nil {
		return nil, err
	}
	for _, e := range listed {
		if byBank[e[0]], err = domains(e[1]); err != nil {
			return nil, err
		}
	}
	out := make([]Bank, len(labels))
	for i, e := range labels {
		out[i] = Bank{ID: e[0], Label: e[1], Senders: append([]string{}, byBank[e[0]]...)}
	}
	return out, nil
}

func BankIDs(banks []Bank) []string {
	out := make([]string, len(banks))
	for i, b := range banks {
		out[i] = b.ID
	}
	return out
}

func (a *Account) labelFrom(banks []Bank) {
	a.BankLabel = a.Bank
	for _, b := range banks {
		if b.ID == a.Bank {
			a.BankLabel = b.Label
		}
	}
}

func LoadCurrencies(ctx context.Context, q db.Querier) (account, tx []string, err error) {
	r := &reader{ctx: ctx, q: q}
	held, all := r.text(CurrenciesKey), r.text(TxCurrenciesKey)
	if r.err != nil {
		return nil, nil, r.err
	}
	if account, err = codes(held); err != nil {
		return nil, nil, err
	}
	tx, err = codes(all)
	return account, tx, err
}

type FX struct {
	BBDPerUSD   float64
	BBDForexFee float64
}

func LoadFX(ctx context.Context, q db.Querier) (FX, error) {
	r := &reader{ctx: ctx, q: q}
	out := FX{BBDPerUSD: r.float(BBDPerUSDKey), BBDForexFee: r.float(BBDForexFeeKey)}
	return out, r.err
}

func LoadCBTTURL(ctx context.Context, q db.Querier) (string, error) {
	r := &reader{ctx: ctx, q: q}
	out := r.text(CBTTURLKey)
	return out, r.err
}

type BillRules struct {
	Days      map[string]int
	Slack     map[string]int
	Tolerance float64
}

func LoadBillRules(ctx context.Context, q db.Querier) (BillRules, error) {
	r := &reader{ctx: ctx, q: q}
	out := BillRules{
		Days:      map[string]int{Weekly: r.integer(WeeklyDaysKey), Monthly: r.integer(MonthlyDaysKey), Yearly: r.integer(YearlyDaysKey)},
		Slack:     map[string]int{Weekly: r.integer(WeeklySlackKey), Monthly: r.integer(MonthlySlackKey), Yearly: r.integer(YearlySlackKey)},
		Tolerance: r.float(BillToleranceKey),
	}
	return out, r.err
}

type Merge struct {
	Tolerance float64
	Days      int
}

func LoadMerge(ctx context.Context, q db.Querier) (Merge, error) {
	r := &reader{ctx: ctx, q: q}
	out := Merge{Tolerance: r.float(MergeToleranceKey), Days: r.integer(MergeDaysKey)}
	return out, r.err
}

type Scanning struct {
	BackfillDays int
	BodyChars    int
}

func LoadScanning(ctx context.Context, q db.Querier) (Scanning, error) {
	r := &reader{ctx: ctx, q: q}
	out := Scanning{BackfillDays: r.integer(BackfillDaysKey), BodyChars: r.integer(BodyCharsKey)}
	return out, r.err
}

func ScanLimit(ctx context.Context, q db.Querier) (int, error) {
	r := &reader{ctx: ctx, q: q}
	out := r.integer(ScanLimitKey)
	return out, r.err
}

func CLIScanLimit(ctx context.Context, q db.Querier) (int, error) {
	r := &reader{ctx: ctx, q: q}
	out := r.integer(CLIScanLimitKey)
	return out, r.err
}

type PageLimits struct {
	Unassigned   int
	Transactions int
	BillsSoon    int
}

func LoadPageLimits(ctx context.Context, q db.Querier) (PageLimits, error) {
	r := &reader{ctx: ctx, q: q}
	out := PageLimits{Unassigned: r.integer(UnassignedLimitKey), Transactions: r.integer(TransactionLimitKey), BillsSoon: r.integer(BillsSoonDaysKey)}
	return out, r.err
}

func senderDefaults(banks []Bank, id string) []string {
	for _, b := range banks {
		if b.ID == id {
			return b.Senders
		}
	}
	return nil
}
