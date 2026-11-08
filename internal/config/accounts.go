package config

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const KeyMailAccountsFile = "mail_accounts_file"

type AccountSpec struct {
	Name     string
	Label    string
	Preset   string
	Login    string
	Password string
	Enabled  bool
	Stored   bool
}

func (a AccountSpec) Host() string { return presets[a.Preset].host }

func (a AccountSpec) SMTPHost() string { return presets[a.Preset].smtpHost }

func (a AccountSpec) UncertainDest() string { return presets[a.Preset].uncertainDest }

type preset struct{ host, smtpHost, uncertainDest string }

var presets = map[string]preset{
	ICloud: {"imap.mail.me.com", "smtp.mail.me.com", "Misc"},
	Gmail:  {"imap.gmail.com", "smtp.gmail.com", ""},
}

var accountID = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func init() {
	settings.MustRegister(settings.Deploy(settings.Def{
		Key: KeyMailAccountsFile, Module: "mail", Type: settings.String}))
}

func Accounts() []AccountSpec {
	return loadState().accounts
}

func Account(name string) (AccountSpec, bool) {
	for _, a := range Accounts() {
		if a.Name == name {
			return a, true
		}
	}
	return AccountSpec{}, false
}

func Preset(account string) string {
	a, _ := Account(account)
	return a.Preset
}

func UseAccounts(list []AccountSpec) (restore func()) {
	previous := loadState()
	next := *previous
	next.fileAccounts, next.accounts = list, list
	current.Store(&next)
	return func() { current.Store(previous) }
}

func PasswordKey(id string) string { return "mail_password_" + id }

func RegisterPassword(id string) error {
	if _, ok := settings.Default.Lookup(PasswordKey(id)); ok {
		return nil
	}
	return settings.Default.Register(settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{
		Key: PasswordKey(id), Module: "mail", Type: settings.String, Page: "/settings/mail"}))
}

func LoadStoredAccounts(ctx context.Context, q db.Querier) error {
	s := loadState()
	list, err := mergeStoredAccounts(ctx, q, s.fileAccounts)
	if err != nil {
		return err
	}
	next := *s
	next.accounts = list
	current.Store(&next)
	return nil
}

func mergeStoredAccounts(ctx context.Context, q db.Querier, base []AccountSpec) ([]AccountSpec, error) {
	list := slices.Clone(base)
	rows, err := q.QueryContext(ctx, "SELECT id, label, preset, login, enabled FROM mail_accounts ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	type row struct {
		id, label, preset, login string
		enabled                  bool
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.label, &x.preset, &x.login, &x.enabled)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	for _, x := range stored {
		if slices.ContainsFunc(list, func(a AccountSpec) bool { return a.Name == x.id }) {
			continue
		}
		if err := RegisterPassword(x.id); err != nil {
			return nil, err
		}
		password, err := secrets.NewIn(nil, q, nil).Value(ctx, PasswordKey(x.id))
		if err != nil {
			return nil, err
		}
		a, err := NewAccount(x.id, x.label, x.preset, x.login, password, x.enabled)
		if err != nil {
			return nil, fmt.Errorf("config: stored mail account %q: %w", x.id, err)
		}
		a.Stored = true
		list = append(list, a)
	}
	return list, nil
}

func NewAccount(name, label, presetName, login, password string, enabled bool) (AccountSpec, error) {
	_, ok := presets[presetName]
	if !ok {
		return AccountSpec{}, fmt.Errorf("preset must be %s or %s", ICloud, Gmail)
	}
	if len(name) > 32 || !accountID.MatchString(name) {
		return AccountSpec{}, errors.New("id must be up to 32 lowercase letters, digits and single underscores, starting with a letter")
	}
	if label == "" {
		label = name
	}
	return AccountSpec{
		Name: name, Label: label, Preset: presetName, Login: py.Strip(login),
		Password: strings.Join(py.Split(password), ""), Enabled: enabled,
	}, nil
}

type accountEntry struct {
	ID           string `toml:"id"`
	Label        string `toml:"label"`
	Preset       string `toml:"preset"`
	Login        string `toml:"login"`
	PasswordFile string `toml:"password_file"`
	Enabled      *bool  `toml:"enabled"`
}

func loadAccounts(env Env) ([]AccountSpec, error) {
	path, ok := pinned(env, KeyMailAccountsFile)
	if !ok {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", KeyMailAccountsFile, err)
	}
	var doc struct {
		Account []accountEntry `toml:"account"`
	}
	meta, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&doc)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		return nil, fmt.Errorf("config: %s: unknown key %q", path, extra[0].String())
	}
	var list []AccountSpec
	for i, e := range doc.Account {
		where := fmt.Sprintf("%s: account %d", path, i+1)
		if e.ID != "" {
			where = fmt.Sprintf("%s: account %q", path, e.ID)
		}
		if slices.ContainsFunc(list, func(a AccountSpec) bool { return a.Name == e.ID }) {
			return nil, fmt.Errorf("config: %s: duplicate id", where)
		}
		if py.Strip(e.Login) == "" || e.PasswordFile == "" {
			return nil, fmt.Errorf("config: %s: login and password_file are required", where)
		}
		passwordFile, err := scalar(env, where+" password_file", e.PasswordFile)
		if err != nil {
			return nil, err
		}
		password, err := os.ReadFile(passwordFile)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", where, err)
		}
		if py.Strip(string(password)) == "" {
			return nil, fmt.Errorf("config: %s: %s is empty", where, passwordFile)
		}
		a, err := NewAccount(e.ID, e.Label, e.Preset, e.Login, string(password), e.Enabled == nil || *e.Enabled)
		if err != nil {
			return nil, fmt.Errorf("config: %s: %w", where, err)
		}
		logx.AddSecret(a.Password)
		list = append(list, a)
	}
	return list, nil
}
