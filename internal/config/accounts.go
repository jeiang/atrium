package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/settings"
)

const KeyMailAccountsFile = "mail_accounts_file"

type AccountSpec struct {
	Name          string
	Label         string
	Preset        string
	Login         string
	Password      string
	Enabled       bool
	Host          string
	SMTPHost      string
	UncertainDest string
}

type preset struct{ host, smtpHost, uncertainDest string }

var presets = map[string]preset{
	ICloud: {"imap.mail.me.com", "smtp.mail.me.com", "Misc"},
	Gmail:  {"imap.gmail.com", "smtp.gmail.com", ""},
}

var (
	liveAccounts atomic.Pointer[[]AccountSpec]
	accountID    = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
)

func init() {
	settings.MustRegister(settings.Def{
		Key: KeyMailAccountsFile, Module: "mail", Type: settings.String, Class: settings.Bootstrap, Restart: true,
	})
}

func Accounts() []AccountSpec {
	if list := liveAccounts.Load(); list != nil {
		return *list
	}
	return nil
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
	previous := liveAccounts.Swap(&list)
	return func() { liveAccounts.Store(previous) }
}

func NewAccount(name, label, presetName, login, password string, enabled bool) (AccountSpec, error) {
	p, ok := presets[presetName]
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
		Host: p.host, SMTPHost: p.smtpHost, UncertainDest: p.uncertainDest,
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
		list = append(list, a)
	}
	return list, nil
}
