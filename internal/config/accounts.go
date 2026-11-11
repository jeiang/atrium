package config

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/mail/accounts"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const KeyMailAccountsFile = "mail_accounts_file"

type AccountSpec struct {
	Name       string
	Label      string
	Preset     string
	Login      string
	Enabled    bool
	Pinned     bool
	AuthMethod AuthMethod
}

func (a AccountSpec) Host() string { return presets[a.Preset].host }

// IMAPEndpoint returns the dial address and TLS server name for host. The
// ATRIUM_IMAP_ADDR and ATRIUM_IMAP_CA overrides exist for the test entry
// point, which points the binary at a local TLS fake.
func IMAPEndpoint(host string) (addr, serverName string) {
	if v, ok := OS("ATRIUM_IMAP_ADDR"); ok && v != "" {
		if h, _, err := net.SplitHostPort(v); err == nil {
			return v, h
		}
		return v, host
	}
	return net.JoinHostPort(host, strconv.Itoa(IMAPPort)), host
}

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

func (s *State) Accounts() []AccountSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accounts
}

func (s *State) Account(name string) (AccountSpec, bool) {
	for _, a := range s.Accounts() {
		if a.Name == name {
			return a, true
		}
	}
	return AccountSpec{}, false
}

func ExistingAccounts(ctx context.Context, q db.Querier) ([]AccountSpec, error) {
	var out []AccountSpec
	for _, spec := range From(ctx).Accounts() {
		found, err := accounts.Exists(ctx, q, spec.Name)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, spec)
	}
	return out, nil
}

func (s *State) Preset(account string) string {
	a, _ := s.Account(account)
	return a.Preset
}

func (s *State) Password(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.passwords[id]
	return value, ok && value != ""
}

func (s *State) UseAccounts(list []AccountSpec, passwords map[string]string) (restore func()) {
	s.mu.Lock()
	previousAccounts, previousPasswords := s.accounts, s.passwords
	s.accounts, s.passwords = list, passwords
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.accounts, s.passwords = previousAccounts, previousPasswords
		s.mu.Unlock()
	}
}

func PasswordKey(id string) string { return "mail_password_" + id }

func (s *State) RegisterPassword(id string) error {
	if _, ok := s.settings.Lookup(PasswordKey(id)); ok {
		return nil
	}
	return s.settings.Register(settings.Secret(settings.SecretOpts{Encrypted: true}, settings.Def{
		Key: PasswordKey(id), Module: "mail", Type: settings.String}))
}

func NormalizePassword(value string) string { return strings.Join(strings.Fields(value), "") }

func (s *State) LoadStoredAccounts(ctx context.Context, q db.Querier) error {
	s.mu.RLock()
	previous := s.passwords
	s.mu.RUnlock()
	accounts, passwords, err := s.loadAccountsFromDB(ctx, q, previous)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.accounts, s.passwords = accounts, passwords
	s.mu.Unlock()
	return nil
}

func (s *State) loadAccountsFromDB(ctx context.Context, q db.Querier, filePasswords map[string]string) ([]AccountSpec, map[string]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, label, preset, login, enabled, pinned, auth_method FROM mail_accounts ORDER BY created_at, id")
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, label, preset, login string
		enabled, pinned          bool
		method                   string
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.label, &x.preset, &x.login, &x.enabled, &x.pinned, &x.method)
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	accounts := make([]AccountSpec, 0, len(stored))
	passwords := make(map[string]string, len(stored))
	for _, x := range stored {
		password := filePasswords[x.id]
		if !x.pinned {
			if err := s.RegisterPassword(x.id); err != nil {
				return nil, nil, err
			}
			password, err = secrets.NewIn(s.settings, nil, q, nil).Value(ctx, PasswordKey(x.id))
			if err != nil {
				return nil, nil, err
			}
		}
		if password != "" {
			passwords[x.id] = password
		}
		a, err := NewAccount(x.id, x.label, x.preset, x.login, x.enabled)
		if err != nil {
			return nil, nil, fmt.Errorf("config: stored mail account %q: %w", x.id, err)
		}
		if a.AuthMethod, err = ParseAuthMethod(x.method); err != nil {
			return nil, nil, fmt.Errorf("config: stored mail account %q: %w", x.id, err)
		}
		a.Pinned = x.pinned
		accounts = append(accounts, a)
	}
	return accounts, passwords, nil
}

func reconcileAccounts(ctx context.Context, d *db.DB, files []AccountSpec) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		keep := make(map[string]bool, len(files))
		for _, a := range files {
			keep[a.Name] = true
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO mail_accounts (id, label, preset, login, enabled, pinned, auth_method, created_at) VALUES (?, ?, ?, ?, ?, 1, ?, ?) "+
					"ON CONFLICT(id) DO UPDATE SET label = excluded.label, preset = excluded.preset, login = excluded.login, enabled = excluded.enabled, auth_method = excluded.auth_method, pinned = 1",
				a.Name, a.Label, a.Preset, a.Login, a.Enabled, a.AuthMethod, stamp()); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM mail_accounts WHERE pinned = 1")
		if err != nil {
			return err
		}
		pinned, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
			var id string
			return id, r.Scan(&id)
		})
		if err != nil {
			return err
		}
		for _, id := range pinned {
			if keep[id] {
				continue
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM mail_accounts WHERE id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

func NewAccount(name, label, presetName, login string, enabled bool) (AccountSpec, error) {
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
	return AccountSpec{Name: name, Label: label, Preset: presetName, Login: strings.TrimSpace(login), Enabled: enabled, AuthMethod: AuthPassword}, nil
}

type accountEntry struct {
	ID           string `toml:"id"`
	Label        string `toml:"label"`
	Preset       string `toml:"preset"`
	Login        string `toml:"login"`
	PasswordFile string `toml:"password_file"`
	Enabled      *bool  `toml:"enabled"`
}

func loadAccounts(env Env, reg *settings.Registry) ([]AccountSpec, map[string]string, error) {
	path, ok := pinned(reg, env, KeyMailAccountsFile)
	if !ok {
		return nil, nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %s: %w", KeyMailAccountsFile, err)
	}
	var doc struct {
		Account []accountEntry `toml:"account"`
	}
	meta, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&doc)
	if err != nil {
		return nil, nil, fmt.Errorf("config: %s: %w", path, err)
	}
	if extra := meta.Undecoded(); len(extra) > 0 {
		return nil, nil, fmt.Errorf("config: %s: unknown key %q", path, extra[0].String())
	}
	var list []AccountSpec
	passwords := map[string]string{}
	for i, e := range doc.Account {
		where := fmt.Sprintf("%s: account %d", path, i+1)
		if e.ID != "" {
			where = fmt.Sprintf("%s: account %q", path, e.ID)
		}
		for _, a := range list {
			if a.Name == e.ID {
				return nil, nil, fmt.Errorf("config: %s: duplicate id", where)
			}
		}
		if strings.TrimSpace(e.Login) == "" || e.PasswordFile == "" {
			return nil, nil, fmt.Errorf("config: %s: login and password_file are required", where)
		}
		passwordFile, err := scalar(env, where+" password_file", e.PasswordFile)
		if err != nil {
			return nil, nil, err
		}
		password, err := os.ReadFile(passwordFile)
		if err != nil {
			return nil, nil, fmt.Errorf("config: %s: %w", where, err)
		}
		value := NormalizePassword(string(password))
		if value == "" {
			return nil, nil, fmt.Errorf("config: %s: %s is empty", where, passwordFile)
		}
		a, err := NewAccount(e.ID, e.Label, e.Preset, e.Login, e.Enabled == nil || *e.Enabled)
		if err != nil {
			return nil, nil, fmt.Errorf("config: %s: %w", where, err)
		}
		logx.AddSecret(value)
		passwords[a.Name] = value
		list = append(list, a)
	}
	return list, passwords, nil
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }
