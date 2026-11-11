package mailconn

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
)

const Kind connections.Kind = "mail"

const (
	CapabilityIMAP = "imap"
	CapabilitySMTP = "smtp"
)

func ID(account string) connections.ID { return connections.ID("mail/" + account) }

func Prefix(account string) string { return "mail/" + account }

func PasswordSlot(account string) string { return Prefix(account) + "/password" }

func Account(id connections.ID) string { return strings.TrimPrefix(string(id), "mail/") }

func Open(ctx context.Context, q db.Querier, account string) (*connections.Lease, error) {
	if store := connections.DefaultFor(q); store != nil {
		return store.Open(ctx, ID(account))
	}
	return connections.OpenRead(ctx, q, config.From(ctx).Host(), ID(account))
}

func Settings(lease *connections.Lease) map[string]string {
	var cfg map[string]string
	_ = json.Unmarshal(lease.Config(), &cfg)
	return cfg
}

func IMAPHost(lease *connections.Lease, spec config.AccountSpec) string {
	if host := strings.TrimSpace(Settings(lease)["imap_host"]); host != "" {
		return host
	}
	return spec.Host()
}

func SMTPHost(lease *connections.Lease, spec config.AccountSpec) string {
	if host := strings.TrimSpace(Settings(lease)["smtp_host"]); host != "" {
		return host
	}
	return spec.SMTPHost()
}

func Password(lease *connections.Lease, account string) (string, error) {
	value, err := lease.Credential(PasswordSlot(account))
	if err != nil {
		return "", err
	}
	return config.NormalizePassword(value), nil
}

func init() {
	connections.SetAccountSync(&connections.AccountSync{
		Validate:  validateHostAccounts,
		Reconcile: reconcileAccounts,
		Reload:    reloadAccounts,
	})
	config.RegisterHostAccounts(HostAccounts)
}

func HostAccounts(host config.Host) ([]config.AccountSpec, map[string]string, error) {
	var list []config.AccountSpec
	passwords := map[string]string{}
	for _, id := range sortedConnections(host) {
		c := host.Connections[id]
		if c.Kind != string(Kind) {
			continue
		}
		if !strings.HasPrefix(id, "mail/") {
			return nil, nil, fmt.Errorf("config: connection %q of kind mail must be named mail/<account>", id)
		}
		account := Account(connections.ID(id))
		cfg := c.Config
		enabled := true
		if value, ok := cfg["enabled"]; ok {
			enabled = value != "false"
		}
		spec, err := config.NewAccount(account, c.Label, cfg["preset"], cfg["login"], enabled)
		if err != nil {
			return nil, nil, fmt.Errorf("config: connection %q: %w", id, err)
		}
		if method := strings.TrimSpace(cfg["auth_method"]); method != "" {
			if spec.AuthMethod, err = config.ParseAuthMethod(method); err != nil {
				return nil, nil, fmt.Errorf("config: connection %q: %w", id, err)
			}
		}
		if !config.AuthAllowed(spec.Preset, spec.AuthMethod) {
			return nil, nil, fmt.Errorf("config: connection %q: XOAUTH2 is Gmail-only", id)
		}
		spec.Pinned = true
		if spec.AuthMethod == config.AuthPassword {
			file, ok := c.CredentialFiles[PasswordSlot(account)]
			if !ok {
				return nil, nil, fmt.Errorf("config: connection %q: credential_files needs %s", id, PasswordSlot(account))
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return nil, nil, fmt.Errorf("config: connection %q: %w", id, err)
			}
			password := config.NormalizePassword(string(raw))
			if password == "" {
				return nil, nil, fmt.Errorf("config: connection %q: %s is empty", id, file)
			}
			passwords[account] = password
		}
		list = append(list, spec)
	}
	return list, passwords, nil
}

func validateHostAccounts(_ context.Context, host config.Host) error {
	_, _, err := HostAccounts(host)
	return err
}

func reconcileAccounts(ctx context.Context, tx *sql.Tx, host config.Host) error {
	list, _, err := HostAccounts(host)
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(list))
	for _, a := range list {
		keep[a.Name] = true
		if err := config.SavePinnedAccount(ctx, tx, a, string(ID(a.Name)), stamp()); err != nil {
			return err
		}
	}
	pinned, err := config.PinnedAccounts(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range pinned {
		if keep[id] {
			continue
		}
		if err := config.DeleteStoredAccount(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

func reloadAccounts(ctx context.Context, store *connections.Store) error {
	d := store.DB()
	if d == nil {
		return nil
	}
	return config.From(ctx).LoadStoredAccounts(ctx, d.Read)
}

func sortedConnections(host config.Host) []string {
	out := make([]string, 0, len(host.Connections))
	for id := range host.Connections {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func stamp() float64 { return float64(time.Now().UnixNano()) / 1e9 }
