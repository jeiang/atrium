package config

import (
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

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/mail/accounts"
)

type AccountSpec struct {
	Name             string
	Label            string
	Preset           string
	Login            string
	Enabled          bool
	Pinned           bool
	AuthMethod       AuthMethod
	ConnectionID     string
	CapabilityPrefix string
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

// HostAccounts derives the accounts declared by host mail connections. The
// mail owner registers it; a process without the mail owner has no host
// accounts.
var hostAccounts func(Host) ([]AccountSpec, map[string]string, error)

func RegisterHostAccounts(f func(Host) ([]AccountSpec, map[string]string, error)) { hostAccounts = f }

// AccountCredential is the stored credential at a connection slot. An empty
// Source means the slot is absent.
type AccountCredential struct {
	Source  string
	Value   string
	FileRef string
}

// accountCredential reads the credential behind an account's connection. The
// connections owner registers it; a process without the connections owner
// reads no stored credential.
var accountCredential func(context.Context, db.Querier, string, string) (AccountCredential, error)

func RegisterAccountCredential(f func(context.Context, db.Querier, string, string) (AccountCredential, error)) {
	accountCredential = f
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
	rows, err := q.QueryContext(ctx,
		"SELECT id, label, preset, login, enabled, pinned, auth_method, connection_id, capability_prefix FROM mail_accounts ORDER BY created_at, id")
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id, label, preset, login string
		enabled, pinned          bool
		method                   string
		connection, prefix       sql.NullString
	}
	stored, err := db.Collect(rows, func(r *sql.Rows) (row, error) {
		var x row
		err := r.Scan(&x.id, &x.label, &x.preset, &x.login, &x.enabled, &x.pinned, &x.method, &x.connection, &x.prefix)
		return x, err
	})
	if err != nil {
		return nil, nil, err
	}
	accounts := make([]AccountSpec, 0, len(stored))
	passwords := make(map[string]string, len(stored))
	for _, x := range stored {
		connection := x.connection.String
		if connection == "" {
			connection = "mail/" + x.id
		}
		prefix := x.prefix.String
		if prefix == "" {
			prefix = "mail/" + x.id
		}
		password := filePasswords[x.id]
		if accountCredential != nil {
			c, err := accountCredential(ctx, q, connection, prefix+"/password")
			if err != nil {
				return nil, nil, fmt.Errorf("config: mail account %q credential: %w", x.id, err)
			}
			switch c.Source {
			case "database":
				if c.Value != "" {
					password = c.Value
				}
			case "host_file":
				raw, err := os.ReadFile(c.FileRef)
				if err != nil {
					return nil, nil, fmt.Errorf("config: mail account %q credential: %w", x.id, err)
				}
				if value := NormalizePassword(string(raw)); value != "" {
					password = value
				}
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
		a.ConnectionID = connection
		a.CapabilityPrefix = prefix
		accounts = append(accounts, a)
	}
	return accounts, passwords, nil
}

func reconcileAccounts(ctx context.Context, d *db.DB, files []AccountSpec) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		keep := make(map[string]bool, len(files))
		for _, a := range files {
			keep[a.Name] = true
			if err := SavePinnedAccount(ctx, tx, a, "", stamp()); err != nil {
				return err
			}
		}
		pinned, err := PinnedAccounts(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range pinned {
			if keep[id] {
				continue
			}
			if err := DeleteStoredAccount(ctx, tx, id); err != nil {
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

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }
