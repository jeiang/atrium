package config

import (
	"context"
	"sync"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type State struct {
	mu           sync.RWMutex
	core         core
	accounts     []AccountSpec
	passwords    map[string]string
	settings     *settings.Registry
	host         Host
	reconcile    func(context.Context, *db.DB, Host) error
	credential   AccountCredentialFunc
	hostAccounts HostAccountsFunc
	dbKey        db.Key
}

func NewState(reg *settings.Registry) *State {
	return &State{settings: reg}
}

func (s *State) SetDBKey(k db.Key) {
	s.mu.Lock()
	s.dbKey = k
	s.mu.Unlock()
}

func (s *State) DBKey() db.Key {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dbKey
}

func (s *State) SetConnectionReconciler(f func(context.Context, *db.DB, Host) error) {
	s.mu.Lock()
	s.reconcile = f
	s.mu.Unlock()
}

func (s *State) Host() Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host
}

func (s *State) HostValue(key string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.host.Values[key]
}

type stateKey struct{}

type envKey struct{}

func WithEnv(ctx context.Context, env Env) context.Context {
	return context.WithValue(ctx, envKey{}, env)
}

func EnvFrom(ctx context.Context) Env {
	env, _ := ctx.Value(envKey{}).(Env)
	if env == nil {
		panic("config: no environment in context")
	}
	return env
}

func With(ctx context.Context, s *State) context.Context {
	return context.WithValue(ctx, stateKey{}, s)
}

func SetHostFrom(ctx context.Context, h Host) {
	if s, ok := ctx.Value(stateKey{}).(*State); ok && s != nil {
		s.mu.Lock()
		s.host = h
		s.mu.Unlock()
	}
}

func HostValueFrom(ctx context.Context, key string) string {
	if s, ok := ctx.Value(stateKey{}).(*State); ok && s != nil {
		return s.HostValue(key)
	}
	return ""
}

func From(ctx context.Context) *State {
	s, _ := ctx.Value(stateKey{}).(*State)
	if s == nil {
		panic("config: no state in context")
	}
	return s
}

func OpenStore(ctx context.Context, env Env) (*db.DB, *settings.Store, error) {
	cfg, err := Load(env)
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Connect(cfg.DBPath, db.WithKey(From(ctx).DBKey()))
	if err != nil {
		return nil, nil, err
	}
	if err := Boot(ctx, env, d, cfg); err != nil {
		_ = d.Close()
		return nil, nil, err
	}
	return d, settings.NewStore(settings.From(ctx), d, d), nil
}

func Boot(ctx context.Context, env Env, d *db.DB, cfg Config) error {
	return From(ctx).Boot(ctx, env, d, cfg)
}

func (s *State) Boot(ctx context.Context, env Env, d *db.DB, cfg Config) error {
	reg := s.settings
	host, err := LoadHost(env, reg, HostFile(env))
	if err != nil {
		return err
	}
	if reg.PinSource() == nil {
		pins, err := LoadPins(env, reg)
		if err != nil {
			return err
		}
		reg.SetPins(CombinePins(host, pins))
	}
	s.mu.Lock()
	s.host = host
	s.mu.Unlock()
	if err := db.Migrate(ctx, d, cfg.DBPath); err != nil {
		return err
	}
	if err := settings.NewStore(reg, d, d).Load(ctx); err != nil {
		return err
	}
	s.mu.RLock()
	reconcile, hostAccounts := s.reconcile, s.hostAccounts
	s.mu.RUnlock()
	if reconcile != nil {
		if err := reconcile(ctx, d, host); err != nil {
			return err
		}
	}
	if initial := stripped(env, InitialTZEnv); initial != "" {
		if _, err := reg.Seed(ctx, d.Write, KeyTZ, initial); err != nil {
			return err
		}
	}
	c, err := loadCore(ctx, d.Read)
	if err != nil {
		return err
	}
	var (
		files         []AccountSpec
		filePasswords map[string]string
	)
	if hostAccounts != nil {
		if files, filePasswords, err = hostAccounts(host); err != nil {
			return err
		}
	}
	if err := reconcileAccounts(ctx, d, files); err != nil {
		return err
	}
	accounts, passwords, err := s.loadAccountsFromDB(ctx, d.Read, filePasswords)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.core, s.accounts, s.passwords = c, accounts, passwords
	s.mu.Unlock()
	return nil
}
