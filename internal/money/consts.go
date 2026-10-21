package money

import (
	"github.com/jeiang/atrium/internal/httpx"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/rx"
)

var log = logx.Logger("atrium.money")

const Republic = "republic"

const (
	BankAccount = "bank"
	CreditCard  = "credit_card"
)

var AccountKinds = []string{BankAccount, CreditCard}

const (
	TTD = "TTD"
	BBD = "BBD"
	USD = "USD"
)

const (
	Purchase     = "purchase"
	ATM          = "atm_withdrawal"
	Transfer     = "transfer"
	CardPayment  = "card_payment"
	Refund       = "refund"
	Statement    = "statement"
	FXAdjustment = "fx_adjustment"
	None         = "none"
)

var (
	ExtractKinds  = []string{Purchase, ATM, Transfer, CardPayment, Refund, Statement, None}
	SpendingKinds = []string{Purchase, ATM, Refund, FXAdjustment}
	TransferKinds = []string{Transfer, CardPayment}
)

const (
	BankAlert     = "bank_alert"
	Receipt       = "receipt"
	StatementRole = "statement"
)

const (
	Exact  = "exact"
	Prefix = "prefix"
	Regex  = "regex"
)

var AliasKinds = []string{Exact, Prefix, Regex}

const (
	ForceBill = "bill"
	NotBill   = "not_bill"
)

var Overrides = []string{ForceBill, NotBill}

const (
	Weekly  = "weekly"
	Monthly = "monthly"
	Yearly  = "yearly"
	Unknown = "unknown"
)

var Cadences = []string{Weekly, Monthly, Yearly, Unknown}

var cadenceOrder = []string{Weekly, Monthly, Yearly}

const (
	Cash             = "Cash"
	Fees             = "Fees"
	FXCategory       = "FX adjustment"
	Income           = "Income"
	TransferCategory = "Transfer"
	Other            = "Other"
)

const (
	UndoOp      = "money_restore"
	SinceKey    = "money_since"
	AllRows     = 100000
	DefaultList = 100
)

var snapshotTables = []string{"payees", "payee_aliases", "transactions", "transaction_sources"}

type LedgerError string

func (e LedgerError) Error() string { return string(e) }

func (LedgerError) PyClass() string { return "LedgerError" }

func (LedgerError) Is(target error) bool { return target == httpx.ErrValue }

var (
	storeNumberRE   = rx.MustCompile(`#\s*\d+|\b\d{3,}\b`, 0)
	nonDescriptorRE = rx.MustCompile(`[^A-Z0-9&'.\- ]+`, 0)
	nonDigitRE      = rx.MustCompile(`\D`, 0)
	patternSplitRE  = rx.MustCompile(`[,\s]+`, 0)
	isoDateRE       = rx.MustCompile(`\d{4}-\d{2}-\d{2}`, 0)
	periodMonthRE   = rx.MustCompile(`\d{4}-\d{2}`, 0)
	periodYearRE    = rx.MustCompile(`\d{4}`, 0)
	periodDaysRE    = rx.MustCompile(`\d+d`, 0)
	periodRangeRE   = rx.MustCompile(`(\d{4}-\d{2}-\d{2})/(\d{4}-\d{2}-\d{2})`, 0)
)

var countryCodes = map[string]bool{
	"US": true, "USA": true, "TT": true, "TTO": true, "BB": true, "BRB": true, "GB": true, "UK": true, "CA": true,
	"CAN": true, "AU": true, "AUS": true, "DE": true, "FR": true, "NL": true, "IE": true, "SG": true, "ES": true,
	"IT": true, "JP": true, "IN": true, "MX": true, "BR": true, "JM": true, "GY": true,
}
