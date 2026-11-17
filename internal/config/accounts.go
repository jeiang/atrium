package config

import (
	"context"
	"strings"

	"github.com/jeiang/atrium/internal/db"
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

// HostAccountsFunc derives the accounts declared by host mail connections.
// The mail owner declares it; a process without the mail owner has no host
// accounts.
type HostAccountsFunc = func(Host) ([]AccountSpec, map[string]string, error)

func (s *State) SetHostAccounts(f HostAccountsFunc) {
	s.mu.Lock()
	s.hostAccounts = f
	s.mu.Unlock()
}

type AccountRowsFunc = func(context.Context, db.Querier, map[string]string, AccountCredentialFunc) ([]AccountSpec, map[string]string, error)

func (s *State) SetAccountRows(f AccountRowsFunc) {
	s.mu.Lock()
	s.accountRows = f
	s.mu.Unlock()
}

// AccountCredential is the stored credential at a connection slot. An empty
// Source means the slot is absent.
type AccountCredential struct {
	Source  string
	Value   string
	FileRef string
}

type AccountCredentialFunc = func(context.Context, db.Querier, string, string) (AccountCredential, error)

// SetAccountCredential installs the reader for the credential behind an
// account's connection. A process without the connections owner reads no
// stored credential.
func (s *State) SetAccountCredential(f AccountCredentialFunc) {
	s.mu.Lock()
	s.credential = f
	s.mu.Unlock()
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
	s.accountsChanged()
	return func() {
		s.mu.Lock()
		s.accounts, s.passwords = previousAccounts, previousPasswords
		s.mu.Unlock()
		s.accountsChanged()
	}
}

func NormalizePassword(value string) string { return strings.Join(strings.Fields(value), "") }

func (s *State) LoadStoredAccounts(ctx context.Context, q db.Querier) error {
	s.mu.RLock()
	previous := s.passwords
	s.mu.RUnlock()
	accounts, passwords, err := s.loadAccountRows(ctx, q, previous)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.accounts, s.passwords = accounts, passwords
	s.mu.Unlock()
	s.accountsChanged()
	return nil
}

func (s *State) OnAccounts(fn func()) (remove func()) {
	key := &fn
	s.mu.Lock()
	if s.hooks == nil {
		s.hooks = map[*func()]struct{}{}
	}
	s.hooks[key] = struct{}{}
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.hooks, key)
		s.mu.Unlock()
	}
}

func (s *State) accountsChanged() {
	s.mu.RLock()
	hooks := make([]func(), 0, len(s.hooks))
	for fn := range s.hooks {
		hooks = append(hooks, *fn)
	}
	s.mu.RUnlock()
	for _, fn := range hooks {
		fn()
	}
}

func (s *State) loadAccountRows(ctx context.Context, q db.Querier, filePasswords map[string]string) ([]AccountSpec, map[string]string, error) {
	s.mu.RLock()
	rows, credential := s.accountRows, s.credential
	s.mu.RUnlock()
	if rows == nil {
		return []AccountSpec{}, map[string]string{}, nil
	}
	return rows(ctx, q, filePasswords, credential)
}
