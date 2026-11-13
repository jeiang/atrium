package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/application/state"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	vecindex "github.com/jeiang/atrium/internal/embed"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/mailconn"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	AccountsName      application.Name = "mail.accounts"
	AccountAddName    application.Name = "mail.account-add"
	AccountUpdateName application.Name = "mail.account-update"
	AccountTestName   application.Name = "mail.account-test"
	AccountRemoveName application.Name = "mail.account-remove"
)

type refusal string

func (e refusal) Error() string { return string(e) }

type Account struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Preset      string `json:"preset"`
	Login       string `json:"login"`
	Enabled     bool   `json:"enabled"`
	Pinned      bool   `json:"pinned"`
	PasswordSet bool   `json:"password_set"`
	AuthMethod  string `json:"auth_method"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
}

type AccountsResult struct {
	Accounts []Account            `json:"accounts"`
	Presets  []string             `json:"presets"`
	Indexes  []vecindex.Index     `json:"indexes"`
	Fields   []settings.FieldView `json:"fields"`
	Failed   string               `json:"failed"`
	Error    string               `json:"error"`
	Form     map[string]string    `json:"form"`
}

var MailFields = []settings.Field{
	{Key: mail.KeyUncertainDest, Label: "Uncertain destination"},
	{Key: mail.KeyLoopsRemindHour, Label: "Loop reminder hour", Number: true, Whole: true},
}

type AccountAddInput struct{ ID, Label, Preset, Login, AuthMethod, Password string }
type AccountUpdateInput struct {
	ID                       string
	Label, Login, AuthMethod *string
	Password                 string
	Enabled                  bool
}
type AccountInput struct{ ID string }
type AccountResult struct{ ID string }
type AccountTestResult struct {
	OK     bool
	Reason string
}

func DescribeAccounts() application.Definition {
	return application.Describe[struct{}, AccountsResult](AccountsName, application.Query, []application.Authority{application.Operator}, application.NoEffect, false)
}
func DescribeAccountAdd() application.Definition {
	return application.Describe[AccountAddInput, AccountResult](AccountAddName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeAccountUpdate() application.Definition {
	return application.Describe[AccountUpdateInput, AccountResult](AccountUpdateName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeAccountTest() application.Definition {
	return application.Describe[AccountInput, AccountTestResult](AccountTestName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func DescribeAccountRemove() application.Definition {
	return application.Describe[AccountInput, AccountResult](AccountRemoveName, application.Command, []application.Authority{application.Operator}, application.NoEffect, true)
}
func (s *Service) connectionStore(ctx context.Context) *connections.Store {
	if store := connections.DefaultFor(s.db.Read); store != nil {
		return store
	}
	reg, err := connections.From(ctx)
	if err != nil {
		return nil
	}
	store, err := connections.New(s.db, config.From(ctx).Host(), reg)
	if err != nil {
		return nil
	}
	connections.SetDefault(s.db.Read, store)
	return store
}

func (s *Service) accountList(ctx context.Context) ([]Account, error) {
	store := s.connectionStore(ctx)
	var out []Account
	for _, a := range config.From(ctx).Accounts() {
		password, _ := config.From(ctx).Password(a.Name)
		v := Account{ID: a.Name, Label: a.Label, Preset: a.Preset, Login: a.Login, Enabled: a.Enabled, Pinned: a.Pinned,
			PasswordSet: password != "", AuthMethod: string(a.AuthMethod), State: string(connections.Untested)}
		if store != nil {
			view, err := store.Get(ctx, mailconn.ID(a.Name))
			switch {
			case errors.As(err, new(connections.UnknownID)):
			case err != nil:
				return nil, err
			default:
				v.State, v.Reason = string(view.State), view.Reason
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) stored(ctx context.Context, id string) (config.AccountSpec, error) {
	a, ok := config.From(ctx).Account(id)
	switch {
	case !ok:
		return a, errNotFound
	case a.Pinned:
		return a, refusal(id + " is set by deployment")
	}
	return a, nil
}

var errNotFound = errors.New("not found")

func candidate(a config.AccountSpec, password string) connections.Candidate {
	cfg, _ := json.Marshal(map[string]string{
		"preset":      a.Preset,
		"login":       a.Login,
		"auth_method": string(a.AuthMethod),
		"enabled":     strconv.FormatBool(a.Enabled),
	})
	c := connections.Candidate{
		Label:  a.Label,
		Config: cfg,
		Capabilities: []connections.Capability{
			{Name: mailconn.CapabilityIMAP, Enabled: true},
			{Name: mailconn.CapabilitySMTP, Enabled: true},
		},
	}
	if password != "" {
		c.Credentials = []connections.CredentialInput{{Slot: mailconn.PasswordSlot(a.Name), Value: password}}
	}
	return c
}

func (s *Service) ensure(ctx context.Context, a config.AccountSpec) (created bool, err error) {
	store := s.connectionStore(ctx)
	if store == nil {
		return false, errors.New("connections are not available")
	}
	_, err = store.Get(ctx, mailconn.ID(a.Name))
	if err == nil {
		return false, nil
	}
	if !errors.As(err, new(connections.UnknownID)) {
		return false, err
	}
	return true, store.Ensure(ctx, mailconn.ID(a.Name), mailconn.Kind, a.Label, nil, candidate(a, "").Capabilities)
}

func (s *Service) drop(ctx context.Context, a config.AccountSpec) {
	if store := s.connectionStore(ctx); store != nil {
		_ = store.Remove(ctx, mailconn.ID(a.Name))
	}
}

func (s *Service) save(ctx context.Context, a config.AccountSpec) error {
	if err := s.db.WithTx(ctx, func(tx *sql.Tx) error {
		return config.SaveStoredAccount(ctx, tx, a, s.now())
	}); err != nil {
		return err
	}
	return config.From(ctx).LoadStoredAccounts(ctx, s.db.Read)
}

func (s *Service) Accounts(ctx context.Context, in struct{}) (application.Read[AccountsResult], error) {
	var out AccountsResult
	list, err := s.accountList(ctx)
	if err != nil {
		return application.Read[AccountsResult]{}, err
	}
	out.Accounts = list
	out.Presets = []string{config.ICloud, config.Gmail}
	if out.Indexes, err = mail.EmbedIndexes(ctx, s.db); err != nil {
		return application.Read[AccountsResult]{}, err
	}
	if out.Fields, err = settings.LoadFields(ctx, s.db.Read, MailFields); err != nil {
		return application.Read[AccountsResult]{}, err
	}
	rev, err := s.snapshot(ctx, AccountsName, in, func(db.Querier) error { return nil })
	return application.Read[AccountsResult]{Revision: rev, Data: out}, err
}
func accountValue(v *string, fallback string) string {
	if v == nil || *v == "" {
		return fallback
	}
	return *v
}
func (s *Service) AccountAdd(ctx context.Context, in AccountAddInput) (application.Change[AccountResult], error) {
	kept := map[string]string{"id": strings.TrimSpace(in.ID), "label": strings.TrimSpace(in.Label), "preset": strings.TrimSpace(in.Preset), "login": strings.TrimSpace(in.Login), "auth_method": strings.TrimSpace(in.AuthMethod)}
	password := config.NormalizePassword(in.Password)
	err := func() error {
		a, err := config.NewAccount(kept["id"], kept["label"], kept["preset"], kept["login"], true)
		if err == nil {
			a.AuthMethod, err = config.ParseAuthMethod(kept["auth_method"])
		}
		switch {
		case err != nil:
			err = refusal(err.Error())
		case !config.AuthAllowed(a.Preset, a.AuthMethod):
			err = refusal("XOAUTH2 is Gmail-only")
		case a.Login == "":
			err = refusal("login is required")
		case a.AuthMethod == config.AuthPassword && password == "":
			err = refusal("login and password are required")
		case !s.db.Encrypted():
			err = settings.EncryptionRequired(mailconn.PasswordSlot(a.Name))
		default:
			if _, exists := config.From(ctx).Account(a.Name); exists {
				err = refusal(a.Name + " is already an account id")
			}
		}
		attempted := false
		if err == nil {
			attempted = true
			_, err = s.ensure(ctx, a)
		}
		if err == nil {
			_, err = s.connectionStore(ctx).TestAndSave(ctx, mailconn.ID(a.Name), candidate(a, password))
		}
		if err == nil {
			err = s.save(ctx, a)
		}
		if err != nil {
			if attempted {
				_ = config.DeleteStoredAccount(ctx, s.db.Write, a.Name)
				s.drop(ctx, a)
				_ = config.From(ctx).LoadStoredAccounts(ctx, s.db.Read)
			}
		}
		return err
	}()
	auditID, err := s.settle(ctx, AccountAddName, strings.TrimSpace(in.ID), err, state.OK)
	return application.Change[AccountResult]{Data: AccountResult{strings.TrimSpace(in.ID)}, AuditID: auditID}, err
}
func (s *Service) AccountUpdate(ctx context.Context, in AccountUpdateInput) (application.Change[AccountResult], error) {
	id := in.ID
	current, err := s.stored(ctx, id)
	var a config.AccountSpec
	if err == nil {
		password := config.NormalizePassword(in.Password)
		login := strings.TrimSpace(accountValue(in.Login, current.Login))
		a, err = config.NewAccount(id, strings.TrimSpace(accountValue(in.Label, current.Label)), current.Preset, login, in.Enabled)
		if err == nil {
			a.AuthMethod, err = config.ParseAuthMethod(accountValue(in.AuthMethod, string(current.AuthMethod)))
		}
		if err == nil && !config.AuthAllowed(a.Preset, a.AuthMethod) {
			err = refusal("XOAUTH2 is Gmail-only")
		}
		if err == nil && a.Login == "" {
			err = refusal("login is required")
		}
		if err == nil && a.AuthMethod == config.AuthPassword && password == "" {
			_, known := config.From(ctx).Password(id)
			if !known {
				err = refusal("login and password are required")
			}
		}
		tested := a.Login != current.Login || password != "" || a.AuthMethod != current.AuthMethod
		if err == nil {
			_, err = s.ensure(ctx, a)
		}
		if err == nil && tested {
			_, err = s.connectionStore(ctx).TestAndSave(ctx, mailconn.ID(id), candidate(a, password))
		} else if err == nil {
			_, err = s.connectionStore(ctx).Save(ctx, mailconn.ID(id), candidate(a, password))
		}
		if err == nil {
			err = s.save(ctx, a)
		}
	}

	auditID, err := s.settle(ctx, AccountUpdateName, id, err, state.OK)
	return application.Change[AccountResult]{Data: AccountResult{id}, AuditID: auditID}, err
}
func (s *Service) AccountRemove(ctx context.Context, in AccountInput) (application.Change[AccountResult], error) {
	id := in.ID
	_, err := s.stored(ctx, id)
	if err == nil {
		err = s.db.WithTx(ctx, func(tx *sql.Tx) error {
			return config.DeleteStoredAccount(ctx, tx, id)
		})
	}
	if err == nil {
		if store := s.connectionStore(ctx); store != nil {
			err = store.Remove(ctx, mailconn.ID(id))
		}
	}
	if err == nil {
		err = config.From(ctx).LoadStoredAccounts(ctx, s.db.Read)
	}

	auditID, err := s.settle(ctx, AccountRemoveName, id, err, state.OK)
	return application.Change[AccountResult]{Data: AccountResult{id}, AuditID: auditID}, err
}
func (s *Service) AccountTest(ctx context.Context, in AccountInput) (application.Change[AccountTestResult], error) {
	var out AccountTestResult
	err := func() error {
		if _, ok := config.From(ctx).Account(in.ID); !ok {
			return errNotFound
		}
		store := s.connectionStore(ctx)
		if store == nil {
			return errors.New("connections are not available")
		}
		view, err := store.Test(ctx, mailconn.ID(in.ID))
		if err != nil {
			return err
		}
		out.OK = view.State == connections.OK
		out.Reason = view.Reason
		if !out.OK && out.Reason == "" {
			out.Reason = "test failed"
		}
		return nil
	}()
	outcome := state.OK
	if !out.OK {
		outcome = state.Failed
	}
	auditID, err := s.settle(ctx, AccountTestName, in.ID, err, outcome)
	return application.Change[AccountTestResult]{Data: out, AuditID: auditID}, err
}
func classifyAccount(err error) error {
	if err == nil {
		return nil
	}
	code := application.Internal
	var why refusal
	var invalid settings.Invalid
	var pinned *settings.PinnedError
	var encryption settings.EncryptionRequired
	var required *connections.EncryptedRequired
	var failedTest *connections.TestFailed
	switch {
	case errors.Is(err, errNotFound):
		code = application.NotFound
	case errors.As(err, &why), errors.As(err, &invalid), errors.As(err, &pinned), errors.As(err, &encryption), errors.As(err, &required), errors.As(err, &failedTest):
		code = application.Refused
	default:
		return err
	}
	return &application.Error{Code: code, Message: err.Error(), Cause: err}
}
func (s *Service) settle(ctx context.Context, name application.Name, target string, err error, outcome state.Outcome) (int64, error) {
	err = classifyAccount(err)
	if err != nil {
		outcome = state.Failed
		var app *application.Error
		if errors.As(err, &app) {
			switch app.Code {
			case application.NotFound:
				outcome = state.NotFound
			case application.Refused:
				outcome = state.Refused
			case application.Invalid:
				outcome = state.Invalid
			case application.Conflict:
				outcome = state.Conflict
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			outcome = state.Canceled
		}
	}
	id, auditErr := s.state.Audit(context.WithoutCancel(ctx), nil, state.Entry{Operation: string(name), Outcome: outcome, Target: target, DetailsJSON: "{}"})
	if auditErr != nil && err == nil {
		committed := &committedAuditError{cause: &connections.CommittedError{Cause: auditErr}}
		logx.Logger("mail.application").Warn("operation committed but audit failed", "operation", string(name), logx.Err, committed)
		return id, committed
	}
	return id, errors.Join(err, auditErr)
}

type committedAuditError struct{ cause error }

func (e *committedAuditError) Error() string { return e.cause.Error() }
func (e *committedAuditError) Unwrap() error { return e.cause }

func Committed(err error) bool {
	var committed *committedAuditError
	return errors.As(err, &committed)
}
