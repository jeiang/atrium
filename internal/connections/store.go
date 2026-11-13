package connections

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const defaultTimeout = 15 * time.Second

var log = logx.Logger("atrium.connections")

type Commit func(context.Context, *sql.Tx, bool) error

func committed(ctx context.Context, tx *sql.Tx, changed bool, commits []Commit) error {
	for _, commit := range commits {
		if err := commit(ctx, tx, changed); err != nil {
			return err
		}
	}
	return nil
}

type Store struct {
	d       *db.DB
	q       db.Querier
	host    config.Host
	reg     *Registry
	now     func() time.Time
	mu      sync.Mutex
	shared  map[string]*shared
	digests map[string]string
}

type shared struct {
	mu      sync.Mutex
	clients map[string]*http.Client
	tokens  map[string]string
	google  *googleTokens
}

type credential struct {
	source  string
	value   string
	fileRef string
	expires *time.Time
}

type transportOptions struct {
	CAFile  string `json:"ca_file,omitempty"`
	Proxy   string `json:"proxy,omitempty"`
	Timeout int    `json:"timeout_seconds,omitempty"`
}

type Lease struct {
	store    *Store
	id       ID
	kind     Kind
	revision int64
	config   json.RawMessage
	options  transportOptions
	caps     []Capability
	creds    map[string]credential
	shared   *shared
	closed   bool
}

func New(d *db.DB, host config.Host, r *Registry) (*Store, error) {
	if d == nil {
		return nil, errors.New("connections: a store needs a database")
	}
	if r == nil {
		return nil, errors.New("connections: a store needs a registry")
	}
	return &Store{d: d, q: d.Read, host: host, reg: r, now: time.Now, shared: map[string]*shared{}, digests: map[string]string{}}, nil
}

func OpenRead(ctx context.Context, q db.Querier, host config.Host, id ID) (*Lease, error) {
	s := &Store{q: q, host: host, now: time.Now, shared: map[string]*shared{}, digests: map[string]string{}}
	return s.open(ctx, q, id)
}

func (s *Store) Open(ctx context.Context, id ID) (*Lease, error) {
	return s.open(ctx, s.q, id)
}

// Ensure materializes an operator-owned built-in connection only when no row
// exists. It never adopts, re-enables, or changes an existing row, so a
// host-owned row the host no longer declares stays disabled.
func (s *Store) Ensure(ctx context.Context, id ID, kind Kind, label string, config map[string]string, caps []Capability) error {
	if s.d == nil {
		return errors.New("connections: built-in materialization needs a writable database")
	}
	if _, ok := s.reg.Lookup(kind); !ok {
		return UnknownKind(kind)
	}
	configJSON, err := json.Marshal(compact(config))
	if err != nil {
		return err
	}
	now := s.stamp()
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) "+
				"VALUES (?, ?, ?, 'operator', ?, '{}', 1, 1, 'untested', ?, ?) ON CONFLICT(id) DO NOTHING",
			id, kind, labelOr(label, string(id)), string(configJSON), now, now)
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 0 {
			return nil
		}
		for _, c := range caps {
			config := c.Config
			if len(config) == 0 {
				config = json.RawMessage("{}")
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, ?, ?, ?) "+
					"ON CONFLICT(connection_id,name) DO NOTHING",
				id, c.Name, boolInt(c.Enabled), string(config)); err != nil {
				return err
			}
		}
		return nil
	})
}

// EnsureCredential returns a credential value, generating and storing it when
// the slot is absent.
func (s *Store) EnsureCredential(ctx context.Context, id ID, slot string, generate func() (string, error)) (string, error) {
	if s.d == nil {
		return "", errors.New("connections: credential generation needs a writable database")
	}
	lease, err := s.open(ctx, s.d.Read, id)
	if err != nil {
		return "", err
	}
	value, err := lease.Credential(slot)
	lease.Close()
	if err == nil {
		return value, nil
	}
	var missing MissingCredential
	if !errors.As(err, &missing) {
		return "", err
	}
	if !s.d.Encrypted() {
		return "", &EncryptedRequired{Slot: slot}
	}
	value, err = generate()
	if err != nil {
		return "", err
	}
	if _, err := s.d.Write.ExecContext(ctx,
		"INSERT INTO connection_credentials (connection_id, slot, source, value, set_at, set_by) VALUES (?, ?, 'database', ?, ?, 'generated') "+
			"ON CONFLICT(connection_id,slot) DO NOTHING",
		id, slot, value, s.stamp()); err != nil {
		return "", err
	}
	lease, err = s.open(ctx, s.d.Read, id)
	if err != nil {
		return "", err
	}
	defer lease.Close()
	return lease.Credential(slot)
}

func (s *Store) ReconcileHost(ctx context.Context) error {
	if s.d == nil {
		return errors.New("connections: host reconciliation needs a writable database")
	}
	if s.reg != nil && s.reg.sync != nil && s.reg.sync.Validate != nil {
		if err := s.reg.sync.Validate(ctx, s.host); err != nil {
			return err
		}
	}
	declared := map[string]bool{}
	for _, id := range sortedKeys(s.host.Connections) {
		declared[id] = true
	}
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range sortedKeys(s.host.Connections) {
			if err := s.reconcileOne(ctx, tx, ID(id), s.host.Connections[id]); err != nil {
				return err
			}
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM connections WHERE config_source = 'host'")
		if err != nil {
			return err
		}
		var stale []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			if !declared[id] {
				stale = append(stale, id)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range stale {
			if _, err := tx.ExecContext(ctx,
				"UPDATE connections SET enabled = 0, state = 'disabled', reason = 'removed from host configuration', revision = revision + 1, updated_at = ? WHERE id = ?",
				s.stamp(), id); err != nil {
				return err
			}
		}
		if s.reg != nil && s.reg.sync != nil && s.reg.sync.Reconcile != nil {
			if err := s.reg.sync.Reconcile(ctx, tx, s.host); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if s.reg != nil && s.reg.sync != nil && s.reg.sync.Reload != nil {
		return s.reg.sync.Reload(ctx, s)
	}
	return nil
}

func (s *Store) reconcileOne(ctx context.Context, tx *sql.Tx, id ID, hc config.HostConnection) error {
	def, ok := s.reg.Lookup(Kind(hc.Kind))
	if !ok {
		return UnknownKind(hc.Kind)
	}
	configJSON, err := json.Marshal(hc.Config)
	if err != nil {
		return err
	}
	transportJSON, err := json.Marshal(transportFrom(hc.Transport))
	if err != nil {
		return err
	}
	caps := make([]Capability, 0, len(hc.Capabilities))
	for _, name := range hc.Capabilities {
		caps = append(caps, Capability{Name: name, Enabled: true, Config: json.RawMessage("{}")})
	}
	cand := Candidate{Label: hc.Label, Config: configJSON, Capabilities: caps}
	if def.Validate != nil {
		if err := def.Validate(cand); err != nil {
			return fmt.Errorf("connections: host %s: %w", id, err)
		}
	}
	var (
		currentConfig, currentTransport string
		currentRevision                 int64
		currentSource                   string
	)
	err = tx.QueryRowContext(ctx,
		"SELECT config_json, transport_json, revision, config_source FROM connections WHERE id = ?", id).
		Scan(&currentConfig, &currentTransport, &currentRevision, &currentSource)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		now := s.stamp()
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connections (id, kind, label, config_source, config_json, transport_json, revision, enabled, state, created_at, updated_at) "+
				"VALUES (?, ?, ?, 'host', ?, ?, 1, 1, 'untested', ?, ?)",
			id, hc.Kind, labelOr(hc.Label, string(id)), string(configJSON), string(transportJSON), now, now); err != nil {
			return err
		}
		currentRevision = 1
	case err != nil:
		return err
	default:
		takeover := currentSource == SourceOperator
		hostOwns := currentSource == SourceHost || takeover
		changed := takeover || currentTransport != string(transportJSON) || hostOwns && currentConfig != string(configJSON)
		if takeover {
			if err := s.refuseCredentialMove(ctx, tx, id, currentConfig, string(configJSON), hc.CredentialFiles); err != nil {
				return err
			}
		}
		revision := currentRevision
		if changed {
			revision++
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET label = ?, transport_json = ?, revision = ?, "+
				"state = CASE WHEN ? THEN 'untested' ELSE state END, "+
				"reason = CASE WHEN ? THEN '' ELSE reason END, "+
				"checked_at = CASE WHEN ? THEN NULL ELSE checked_at END, "+
				"last_ok_at = CASE WHEN ? THEN NULL ELSE last_ok_at END, "+
				"failed_test_revision = CASE WHEN ? THEN NULL ELSE failed_test_revision END, "+
				"results_json = CASE WHEN ? THEN '{}' ELSE results_json END, updated_at = ? "+
				"WHERE id = ?",
			labelOr(hc.Label, string(id)), string(transportJSON), revision, changed, changed, changed, changed, changed, changed, s.stamp(), id); err != nil {
			return err
		}
		if hostOwns && changed {
			if _, err := tx.ExecContext(ctx, "UPDATE connections SET config_json = ?, config_source = 'host' WHERE id = ?", string(configJSON), id); err != nil {
				return err
			}
		}
		currentRevision = revision
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM connection_capabilities WHERE connection_id = ?", id); err != nil {
		return err
	}
	for _, c := range caps {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, ?, ?, ?)",
			id, c.Name, boolInt(c.Enabled), string(c.Config)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM connection_credentials WHERE connection_id = ? AND source = 'host_file'", id); err != nil {
		return err
	}
	for _, slot := range sortedKeys(hc.CredentialFiles) {
		var prior string
		err := tx.QueryRowContext(ctx, "SELECT source FROM connection_credentials WHERE connection_id = ? AND slot = ?", id, slot).Scan(&prior)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if prior == "database" {
			log.Info("the host credential file replaces the database credential", logx.Integration, string(id), "SLOT", slot)
			if _, err := tx.ExecContext(ctx,
				"UPDATE connections SET revision = revision + 1, state = 'untested', reason = '', checked_at = NULL, "+
					"last_ok_at = NULL, failed_test_revision = NULL, results_json = '{}', updated_at = ? WHERE id = ?", s.stamp(), id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM connection_credentials WHERE connection_id = ? AND slot = ?", id, slot); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_credentials (connection_id, slot, source, file_ref, set_at, set_by) VALUES (?, ?, 'host_file', ?, ?, 'host')",
			id, slot, hc.CredentialFiles[slot], s.stamp()); err != nil {
			return err
		}
	}
	_ = currentRevision
	return nil
}

func (s *Store) refuseCredentialMove(ctx context.Context, tx *sql.Tx, id ID, from, to string, files map[string]string) error {
	rows, err := tx.QueryContext(ctx, "SELECT slot FROM connection_credentials WHERE connection_id = ? AND source = 'database'", id)
	if err != nil {
		return err
	}
	slots, err := db.Collect(rows, func(r *sql.Rows) (string, error) {
		var slot string
		return slot, r.Scan(&slot)
	})
	if err != nil {
		return err
	}
	retained := false
	for _, slot := range slots {
		if _, ok := files[slot]; !ok {
			retained = true
			break
		}
	}
	if !retained {
		return nil
	}
	if field, changed := changedEndpoint(from, to); changed {
		return &CredentialEndpointChanged{ID: id, Field: field}
	}
	return nil
}

func changedEndpoint(from, to string) (string, bool) {
	var before, after map[string]string
	_ = json.Unmarshal([]byte(from), &before)
	_ = json.Unmarshal([]byte(to), &after)
	keys := map[string]bool{}
	for key := range before {
		keys[key] = true
	}
	for key := range after {
		keys[key] = true
	}
	for _, key := range sortedKeys(keys) {
		old, ok := endpointIdentity(key, before[key])
		if !ok {
			continue
		}
		if next, _ := endpointIdentity(key, after[key]); next != old {
			return key, true
		}
	}
	return "", false
}

func endpointIdentity(key, value string) (string, bool) {
	name := strings.ToLower(strings.TrimSpace(key))
	if name != "url" && name != "api" && name != "host" && !strings.HasSuffix(name, "_url") && !strings.HasSuffix(name, "_host") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", true
	}
	u, err := url.Parse(value)
	if err != nil {
		return value, true
	}
	if u.Scheme != "" || u.Host != "" {
		return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + u.Port() + strings.TrimRight(u.EscapedPath(), "/"), true
	}
	host, port := value, ""
	if h, p, err := net.SplitHostPort(value); err == nil {
		host, port = h, p
	}
	return "//" + strings.ToLower(host) + ":" + port, true
}

func (s *Store) List(ctx context.Context) ([]View, error) {
	rows, err := s.q.QueryContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, failed_test_revision, results_json "+
			"FROM connections ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []View
	for rows.Next() {
		v, err := s.scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fill(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) Get(ctx context.Context, id ID) (View, error) {
	row := s.q.QueryRowContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, failed_test_revision, results_json "+
			"FROM connections WHERE id = ?", id)
	v, err := s.scanView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return View{}, UnknownID(id)
	}
	if err != nil {
		return View{}, err
	}
	if err := s.fill(ctx, &v); err != nil {
		return View{}, err
	}
	return v, nil
}

// ReadView reads one connection's stored state without a writable store.
func ReadView(ctx context.Context, q db.Querier, id ID) (View, error) {
	s := &Store{q: q}
	row := q.QueryRowContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, failed_test_revision, results_json "+
			"FROM connections WHERE id = ?", id)
	v, err := s.scanView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return View{}, UnknownID(id)
	}
	if err != nil {
		return View{}, err
	}
	if err := s.fill(ctx, &v); err != nil {
		return View{}, err
	}
	return v, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanView(sc scanner) (View, error) {
	var (
		v                 View
		configJSON        string
		transportJSON     string
		resultsJSON       string
		revision          int64
		enabled           int
		checked, lastOK   *float64
		expires, reauthAt *float64
		failedTest        *int64
	)
	if err := sc.Scan(&v.ID, &v.Kind, &v.Label, &v.ConfigSource, &configJSON, &transportJSON, &revision, &enabled, &v.State, &v.Reason,
		&checked, &lastOK, &expires, &reauthAt, &failedTest, &resultsJSON); err != nil {
		return View{}, err
	}
	v.Enabled = enabled != 0
	v.Config = json.RawMessage(configJSON)
	v.revision = revision
	if failedTest != nil {
		v.failedTestRevision = *failedTest
	}
	if err := json.Unmarshal([]byte(transportJSON), &v.transport); err != nil {
		return View{}, fmt.Errorf("connections: %s transport: %w", v.ID, err)
	}
	v.CheckedAt = fromUnix(checked)
	v.LastOKAt = fromUnix(lastOK)
	v.ExpiresAt = fromUnix(expires)
	v.ReauthAt = fromUnix(reauthAt)
	if resultsJSON != "" {
		_ = json.Unmarshal([]byte(resultsJSON), &v.Results)
	}
	if v.Results == nil {
		v.Results = []Result{}
	}
	return v, nil
}

func (v *View) revisionValue() int64 { return v.revision }

func (s *Store) fill(ctx context.Context, v *View) error {
	caps, err := s.capabilities(ctx, s.q, v.ID)
	if err != nil {
		return err
	}
	v.Capabilities = caps
	creds, err := s.credentials(ctx, s.q, v.ID, false)
	if err != nil {
		return err
	}
	v.Credentials = creds
	return nil
}

func (s *Store) capabilities(ctx context.Context, q db.Querier, id ID) ([]CapabilityView, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT name, enabled, config_json FROM connection_capabilities WHERE connection_id = ? ORDER BY name", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CapabilityView
	for rows.Next() {
		var (
			c      CapabilityView
			config string
			on     int
		)
		if err := rows.Scan(&c.Name, &on, &config); err != nil {
			return nil, err
		}
		c.Enabled, c.Config = on != 0, json.RawMessage(config)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) credentials(ctx context.Context, q db.Querier, id ID, withValues bool) ([]CredentialView, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT slot, source, value, file_ref, expires_at, set_at, set_by FROM connection_credentials WHERE connection_id = ? ORDER BY slot", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []CredentialView
	for rows.Next() {
		var (
			c       CredentialView
			value   sql.NullString
			fileRef sql.NullString
			expires sql.NullFloat64
			setAt   float64
		)
		if err := rows.Scan(&c.Slot, &c.Source, &value, &fileRef, &expires, &setAt, &c.SetBy); err != nil {
			return nil, err
		}
		c.Present = value.Valid && value.String != "" || fileRef.Valid && fileRef.String != ""
		c.FileRef = fileRef.String
		if expires.Valid {
			t := fromUnix(&expires.Float64)
			c.ExpiresAt = t
		}
		t := fromUnix(&setAt)
		c.SetAt = t
		out = append(out, c)
	}
	return out, rows.Err()
}

type DisabledConnection struct{ ID ID }

func (e DisabledConnection) Error() string { return "connections: " + string(e.ID) + " is disabled" }

func (e DisabledConnection) Unwrap() error { return UnknownID(e.ID) }

func (s *Store) open(ctx context.Context, q db.Querier, id ID) (*Lease, error) {
	return s.openLease(ctx, q, id, false)
}

func (s *Store) openLease(ctx context.Context, q db.Querier, id ID, allowDisabled bool) (*Lease, error) {
	var (
		v          View
		configJSON string
		transport  string
		revision   int64
		enabled    int
		results    string
		checked    *float64
		lastOK     *float64
		expires    *float64
		reauthAt   *float64
		failedTest *int64
	)
	err := q.QueryRowContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, failed_test_revision, results_json "+
			"FROM connections WHERE id = ?", id).
		Scan(&v.ID, &v.Kind, &v.Label, &v.ConfigSource, &configJSON, &transport, &revision, &enabled, &v.State, &v.Reason,
			&checked, &lastOK, &expires, &reauthAt, &failedTest, &results)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, UnknownID(id)
	}
	if err != nil {
		return nil, err
	}
	if enabled == 0 && !allowDisabled {
		return nil, DisabledConnection{ID: id}
	}
	var opts transportOptions
	if err := json.Unmarshal([]byte(transport), &opts); err != nil {
		return nil, fmt.Errorf("connections: %s transport: %w", id, err)
	}
	caps, err := s.capabilities(ctx, q, id)
	if err != nil {
		return nil, err
	}
	creds, err := s.loadCredentials(ctx, q, id)
	if err != nil {
		return nil, err
	}
	enabledCaps := make([]Capability, 0, len(caps))
	for _, c := range caps {
		enabledCaps = append(enabledCaps, Capability(c))
	}
	if s.d != nil {
		if changed := s.rotate(id, creds); changed {
			if _, err := s.d.Write.ExecContext(ctx,
				"UPDATE connections SET revision = revision + 1, state = 'untested', reason = '', checked_at = NULL, last_ok_at = NULL, failed_test_revision = NULL, results_json = '{}' WHERE id = ?", id); err != nil {
				return nil, err
			}
			revision++
		}
	}
	lease := &Lease{store: s, id: id, kind: v.Kind, revision: revision, config: json.RawMessage(configJSON), options: opts, caps: enabledCaps, creds: creds}
	lease.shared = s.sharedFor(id, revision)
	return lease, nil
}

func (s *Store) rotate(id ID, creds map[string]credential) bool {
	changed := false
	for slot, c := range creds {
		if c.source != "host_file" {
			continue
		}
		digest, err := fileDigest(c.fileRef)
		if err != nil {
			continue
		}
		key := string(id) + "/" + slot
		s.mu.Lock()
		previous, seen := s.digests[key]
		s.digests[key] = digest
		s.mu.Unlock()
		if seen && previous != digest {
			changed = true
		}
	}
	return changed
}

func (s *Store) loadCredentials(ctx context.Context, q db.Querier, id ID) (map[string]credential, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT slot, source, value, file_ref, expires_at FROM connection_credentials WHERE connection_id = ?", id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]credential{}
	for rows.Next() {
		var (
			slot    string
			c       credential
			value   sql.NullString
			fileRef sql.NullString
			expires sql.NullFloat64
		)
		if err := rows.Scan(&slot, &c.source, &value, &fileRef, &expires); err != nil {
			return nil, err
		}
		c.value, c.fileRef = value.String, fileRef.String
		if expires.Valid {
			c.expires = fromUnix(&expires.Float64)
		}
		out[slot] = c
	}
	return out, rows.Err()
}

func (s *Store) sharedFor(id ID, revision int64) *shared {
	key := fmt.Sprintf("%s@%d", id, revision)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.shared == nil {
		s.shared = map[string]*shared{}
	}
	sh, ok := s.shared[key]
	if !ok {
		sh = &shared{clients: map[string]*http.Client{}, tokens: map[string]string{}}
		s.shared[key] = sh
	}
	return sh
}

func (s *Store) stamp() float64 { return float64(s.now().UnixNano()) / 1e9 }

func (l *Lease) Revision() int64            { return l.revision }
func (l *Lease) ID() ID                     { return l.id }
func (l *Lease) Kind() Kind                 { return l.kind }
func (l *Lease) Config() json.RawMessage    { return l.config }
func (l *Lease) Capabilities() []Capability { return l.caps }

func (l *Lease) Endpoint(name string) (string, error) {
	var m map[string]string
	if err := json.Unmarshal(l.config, &m); err != nil {
		return "", fmt.Errorf("connections: %s config: %w", l.id, err)
	}
	value := strings.TrimSpace(m[name])
	if value == "" {
		return "", fmt.Errorf("connections: %s has no %s endpoint", l.id, name)
	}
	return value, nil
}

func (l *Lease) HTTP(name string) (*http.Client, error) {
	return l.http(name, false)
}

// HTTPRead returns a client for a named endpoint whose requests are safe to
// replay after a redirect.
func (l *Lease) HTTPRead(name string) (*http.Client, error) {
	return l.http(name, true)
}

func (l *Lease) http(name string, follow bool) (*http.Client, error) {
	if l.closed {
		return nil, errors.New("connections: the lease is closed")
	}
	endpoint, err := l.Endpoint(name)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("connections: %s endpoint %q must be an http or https URL", l.id, endpoint)
	}
	key := name
	if follow {
		key += "\x00read"
	}
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	if client, ok := l.shared.clients[key]; ok {
		return client, nil
	}
	client, err := l.newClient(0, follow, u)
	if err != nil {
		return nil, err
	}
	l.shared.clients[key] = client
	return client, nil
}

// Client returns a client for a named endpoint with an explicit timeout that
// does not follow redirects.
func (l *Lease) Client(name string, timeout time.Duration) (*http.Client, error) {
	return l.client(name, timeout, false)
}

// ClientRead returns a client for a named endpoint with an explicit timeout
// whose requests are safe to replay after a redirect.
func (l *Lease) ClientRead(name string, timeout time.Duration) (*http.Client, error) {
	return l.client(name, timeout, true)
}

func (l *Lease) client(name string, timeout time.Duration, follow bool) (*http.Client, error) {
	if l.closed {
		return nil, errors.New("connections: the lease is closed")
	}
	endpoint, err := l.Endpoint(name)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("connections: %s endpoint %q must be an http or https URL", l.id, endpoint)
	}
	return l.newClient(timeout, follow, u)
}

func guardOrigin(client *http.Client, base *url.URL) {
	origin := base.Scheme + "://" + base.Host
	prefix := base.Path
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme+"://"+req.URL.Host != origin || !withinPath(prefix, req.URL.Path) {
			req.Header.Del("Authorization")
		}
		return nil
	}
}

func withinPath(base, target string) bool {
	base = path.Clean(base)
	if base == "/" || base == "." {
		return true
	}
	target = path.Clean(target)
	return target == base || strings.HasPrefix(target, base+"/")
}

// DB returns the writable database behind a lease opened by a store; a
// read-only lease has none.
func (l *Lease) DB() *db.DB { return l.store.d }

// DB returns the database a store was opened on.
func (s *Store) DB() *db.DB { return s.d }

// Remove deletes an operator-owned connection and its capabilities and
// credentials. A host-owned connection is disabled instead, because the host
// configuration still declares it.
func (s *Store) Remove(ctx context.Context, id ID) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.ConfigSource == SourceHost {
		return errors.New("connections: " + string(id) + " is declared by the host; remove it there")
	}
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM connections WHERE id = ?", id); err != nil {
			return err
		}
		old := string(current.Config)
		return audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id), Old: &old})
	})
}

func (l *Lease) newClient(timeout time.Duration, follow bool, base *url.URL) (*http.Client, error) {
	if timeout <= 0 {
		timeout = defaultTimeout
		if l.options.Timeout > 0 {
			timeout = time.Duration(l.options.Timeout) * time.Second
		}
	}
	client, err := transport.New(transport.Options{
		Timeout:         timeout,
		CAFile:          l.options.CAFile,
		Proxy:           l.options.Proxy,
		FollowRedirects: follow,
	})
	if err != nil {
		return nil, err
	}
	if follow && base != nil {
		guardOrigin(client, base)
	}
	return l.reporting(client), nil
}

// Transport returns a client for a connection whose destinations are not a
// single declared endpoint. It does not follow redirects.
func (l *Lease) Transport() (*http.Client, error) {
	if l.closed {
		return nil, errors.New("connections: the lease is closed")
	}
	return l.newClient(0, false, nil)
}

func (l *Lease) Credential(slot string) (string, error) {
	c, ok := l.creds[slot]
	if !ok {
		return "", MissingCredential{Slot: slot}
	}
	if c.source == "database" {
		if c.value == "" {
			return "", MissingCredential{Slot: slot}
		}
		return c.value, nil
	}
	raw, err := os.ReadFile(c.fileRef)
	if err != nil {
		return "", fmt.Errorf("connections: %s credential %s: %w", l.id, slot, err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", MissingCredential{Slot: slot}
	}
	return value, nil
}

func (l *Lease) Token(ctx context.Context, capability string) (string, error) {
	if l.kind == KindGoogle {
		return l.googleToken(ctx, capability)
	}
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	if token, ok := l.shared.tokens[capability]; ok {
		return token, nil
	}
	value, err := l.Credential("token/" + capability)
	if err != nil {
		return "", err
	}
	l.shared.tokens[capability] = value
	return value, nil
}

func (l *Lease) InvalidateToken(capability string) {
	if l.kind == KindGoogle {
		tokens := l.googleTokens()
		tokens.mu.Lock()
		defer tokens.mu.Unlock()
		delete(tokens.caps, capability)
		delete(tokens.errs, capability)
		return
	}
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	delete(l.shared.tokens, capability)
}

func (l *Lease) Observe(ctx context.Context, result Result) error {
	if l.store.d == nil {
		return errors.New("connections: observation needs a writable store")
	}
	return l.store.Observe(ctx, l.id, l.revision, result)
}

func (l *Lease) Close() { l.closed = true }

func (s *Store) Test(ctx context.Context, id ID, commits ...Commit) (View, error) {
	lease, err := s.openLease(ctx, s.q, id, true)
	if err != nil {
		return View{}, err
	}
	defer lease.Close()
	def, ok := s.reg.Lookup(lease.kind)
	if !ok {
		return View{}, UnknownKind(lease.kind)
	}
	results, err := def.Test(ctx, lease, lease.caps)
	if err != nil {
		return View{}, err
	}
	return s.record(ctx, id, lease.revision, results, true, commits...)
}

func (s *Store) record(ctx context.Context, id ID, revision int64, results []Result, tested bool, commits ...Commit) (View, error) {
	state, reason := aggregate(results, nil)
	raw, err := json.Marshal(results)
	if err != nil {
		return View{}, err
	}
	now := s.stamp()
	var lastOK, reauthAt, failedTest any
	if state == OK {
		lastOK = now
	}
	if state == NeedsReauth {
		reauthAt = now
	}
	if tested && (state == Failing || state == NeedsReauth) {
		failedTest = revision
	}
	var previous State
	updated := false
	if err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT state FROM connections WHERE id = ? AND revision = ?", id, revision).Scan(&previous); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return committed(ctx, tx, false, commits)
			}
			return err
		}
		res, err := tx.ExecContext(ctx,
			"UPDATE connections SET state = ?, reason = ?, checked_at = ?, last_ok_at = COALESCE(?, last_ok_at), reauth_at = COALESCE(?, reauth_at), results_json = ?, updated_at = ?, "+
				"failed_test_revision = CASE WHEN ? THEN ? ELSE failed_test_revision END WHERE id = ? AND revision = ?",
			state, reason, now, lastOK, reauthAt, string(raw), now, boolInt(tested), failedTest, id, revision)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		updated = n > 0
		return committed(ctx, tx, updated, commits)
	}); err != nil {
		return View{}, err
	}
	if !updated {
		return s.afterWrite(ctx, id)
	}
	s.announce(ctx, id, previous, state, reason)
	return s.afterWrite(ctx, id)
}

// Save writes operator-owned configuration and credentials without running a
// test. It is used by the consent flows, which obtain a grant before any test
// can succeed.
func (s *Store) Save(ctx context.Context, id ID, cand Candidate, commits ...Commit) (View, error) {
	if !audit.HasActor(ctx) {
		return View{}, audit.ErrNoActor
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	def, ok := s.reg.Lookup(current.Kind)
	if !ok {
		return View{}, UnknownKind(current.Kind)
	}
	merged := Candidate{Label: current.Label, Config: current.Config, Credentials: cand.Credentials}
	for _, c := range current.Capabilities {
		merged.Capabilities = append(merged.Capabilities, Capability(c))
	}
	if cand.Label != "" {
		merged.Label = cand.Label
	}
	if cand.Config != nil {
		merged.Config = cand.Config
	}
	if cand.Capabilities != nil {
		merged.Capabilities = cand.Capabilities
	}
	if err := validateCandidate(def, merged, current); err != nil {
		return View{}, err
	}
	if err := s.requireEncrypted(merged); err != nil {
		return View{}, err
	}
	if err := s.save(ctx, id, current, merged, []Result{}, Untested, "", commits...); err != nil {
		return View{}, err
	}
	return s.afterWrite(ctx, id)
}

func (s *Store) TestAndSave(ctx context.Context, id ID, cand Candidate, commits ...Commit) (View, error) {
	if !audit.HasActor(ctx) {
		return View{}, audit.ErrNoActor
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return View{}, err
	}
	def, ok := s.reg.Lookup(current.Kind)
	if !ok {
		return View{}, UnknownKind(current.Kind)
	}
	if err := validateCandidate(def, cand, current); err != nil {
		return View{}, err
	}
	if err := s.requireEncrypted(cand); err != nil {
		return View{}, err
	}
	lease, err := s.candidateLease(ctx, current, cand)
	if err != nil {
		return View{}, err
	}
	results, err := def.Test(ctx, lease, lease.caps)
	if err != nil {
		return View{}, err
	}
	state, reason := aggregate(results, nil)
	if state != OK && state != Expiring {
		view := current
		view.State, view.Reason, view.Results = state, reason, results
		return view, &TestFailed{View: view}
	}
	if err := s.save(ctx, id, current, cand, results, state, reason, commits...); err != nil {
		return View{}, err
	}
	return s.afterWrite(ctx, id)
}

func (s *Store) save(ctx context.Context, id ID, current View, cand Candidate, results []Result, state State, reason string, commits ...Commit) error {
	if err := s.requireEncrypted(cand); err != nil {
		return err
	}
	configJSON := current.Config
	if current.ConfigSource == SourceOperator {
		configJSON = cand.Config
	}
	capsJSON := make([]Capability, 0, len(cand.Capabilities))
	capsJSON = append(capsJSON, cand.Capabilities...)
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return err
	}
	now := s.stamp()
	var previous State
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT revision, state FROM connections WHERE id = ?", id).Scan(&revision, &previous); err != nil {
			return err
		}
		if revision != current.revisionValue() {
			return &Conflict{ID: id, Revision: current.revisionValue()}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET label = ?, config_json = ?, revision = revision + 1, state = ?, reason = ?, checked_at = ?, "+
				"last_ok_at = CASE WHEN ? = 'ok' THEN ? ELSE last_ok_at END, failed_test_revision = NULL, results_json = ?, updated_at = ? WHERE id = ?",
			labelOr(cand.Label, current.Label), string(configJSON), state, reason, now, state, now, string(resultsJSON), now, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM connection_capabilities WHERE connection_id = ?", id); err != nil {
			return err
		}
		for _, c := range capsJSON {
			config := c.Config
			if len(config) == 0 {
				config = json.RawMessage("{}")
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO connection_capabilities (connection_id, name, enabled, config_json) VALUES (?, ?, ?, ?)",
				id, c.Name, boolInt(c.Enabled), string(config)); err != nil {
				return err
			}
		}
		for _, cred := range cand.Credentials {
			if cred.Value == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO connection_credentials (connection_id, slot, source, value, expires_at, set_at, set_by) VALUES (?, ?, 'database', ?, ?, ?, ?) "+
					"ON CONFLICT(connection_id,slot) DO UPDATE SET source = 'database', value = excluded.value, file_ref = NULL, expires_at = excluded.expires_at, set_at = excluded.set_at, set_by = excluded.set_by",
				id, cred.Slot, cred.Value, unix(cred.ExpiresAt), now, actor(ctx)); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id) + "." + cred.Slot, Secret: true}); err != nil {
				return err
			}
		}
		old := string(current.Config)
		next := string(configJSON)
		if old != next {
			if err := audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id), Old: &old, New: &next}); err != nil {
				return err
			}
		}
		return committed(ctx, tx, true, commits)
	})
	if err != nil {
		return err
	}
	s.announce(ctx, id, previous, state, reason)
	return nil
}

func (s *Store) Disconnect(ctx context.Context, id ID, commits ...Commit) error {
	if !audit.HasActor(ctx) {
		return audit.ErrNoActor
	}
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	now := s.stamp()
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM connection_credentials WHERE connection_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET config_json = '{}', enabled = 0, state = 'unconfigured', reason = '', checked_at = NULL, "+
				"last_ok_at = NULL, expires_at = NULL, reauth_at = NULL, failed_test_revision = NULL, results_json = '{}', revision = revision + 1, updated_at = ? WHERE id = ?",
			now, id); err != nil {
			return err
		}
		old := string(current.Config)
		next := "{}"
		if err := audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id), Old: &old, New: &next}); err != nil {
			return err
		}
		return committed(ctx, tx, true, commits)
	})
}

func (s *Store) Observe(ctx context.Context, id ID, revision int64, result Result) error {
	var (
		previous State
		state    State
		reason   string
		updated  bool
	)
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var (
			currentRevision int64
			resultsJSON     string
		)
		if err := tx.QueryRowContext(ctx, "SELECT revision, results_json, state FROM connections WHERE id = ?", id).
			Scan(&currentRevision, &resultsJSON, &previous); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return UnknownID(id)
			}
			return err
		}
		if currentRevision != revision {
			return &Conflict{ID: id, Revision: revision}
		}
		var results []Result
		if resultsJSON != "" {
			_ = json.Unmarshal([]byte(resultsJSON), &results)
		}
		replaced := false
		for i := range results {
			if results[i].Capability == result.Capability {
				results[i] = result
				replaced = true
			}
		}
		if !replaced {
			results = append(results, result)
		}
		state, reason = aggregate(results, nil)
		raw, err := json.Marshal(results)
		if err != nil {
			return err
		}
		now := s.stamp()
		res, err := tx.ExecContext(ctx,
			"UPDATE connections SET state = ?, reason = ?, checked_at = ?, results_json = ?, updated_at = ? WHERE id = ? AND revision = ?",
			state, reason, now, string(raw), now, id, revision)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		updated = n > 0
		return nil
	})
	if err != nil {
		return err
	}
	if updated {
		s.announce(ctx, id, previous, state, reason)
	}
	return nil
}

func (s *Store) candidateLease(ctx context.Context, current View, cand Candidate) (*Lease, error) {
	caps := make([]Capability, 0, len(cand.Capabilities))
	caps = append(caps, cand.Capabilities...)
	creds, err := s.loadCredentials(ctx, s.q, current.ID)
	if err != nil {
		return nil, err
	}
	for _, c := range cand.Credentials {
		if c.Value != "" {
			creds[c.Slot] = credential{source: "database", value: c.Value, expires: c.ExpiresAt}
		}
	}
	config := cand.Config
	if current.ConfigSource == SourceHost {
		config = current.Config
	}
	lease := &Lease{store: s, id: current.ID, kind: current.Kind, revision: current.revisionValue(), config: config, options: current.transport, caps: caps, creds: creds}
	lease.shared = s.sharedFor(current.ID, current.revisionValue())
	return lease, nil
}

func (s *Store) requireEncrypted(cand Candidate) error {
	for _, c := range cand.Credentials {
		if c.Value == "" {
			continue
		}
		if s.d == nil || !s.d.Encrypted() {
			return &EncryptedRequired{Slot: c.Slot}
		}
	}
	return nil
}

func validateCandidate(def Definition, cand Candidate, current View) error {
	if cand.Config == nil {
		cand.Config = json.RawMessage("{}")
	}
	if !json.Valid(cand.Config) {
		return &Invalid{Cause: errors.New("connections: config must be valid JSON")}
	}
	if current.ConfigSource == SourceHost {
		var want, got map[string]string
		_ = json.Unmarshal(current.Config, &want)
		_ = json.Unmarshal(cand.Config, &got)
		if !sameMap(want, got) {
			return &Invalid{Cause: errors.New("connections: the host owns this connection's identity and endpoints")}
		}
	}
	if def.Validate != nil {
		if err := def.Validate(cand); err != nil {
			return &Invalid{Cause: err}
		}
	}
	for _, c := range cand.Credentials {
		if c.HostFileRef != "" {
			return &Invalid{Cause: errors.New("connections: a host file credential is only accepted from host configuration")}
		}
		if c.Value == "" {
			return MissingCredential{Slot: c.Slot}
		}
	}
	return nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func actor(ctx context.Context) string {
	if a := audit.Actor(ctx); a != "" {
		return a
	}
	return "operator"
}

func transportFrom(m map[string]string) transportOptions {
	var o transportOptions
	o.CAFile = m["ca_file"]
	o.Proxy = m["proxy"]
	return o
}

func labelOr(label, fallback string) string {
	if strings.TrimSpace(label) == "" {
		return fallback
	}
	return label
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

var _ = fileDigest

func (s *Store) afterWrite(ctx context.Context, id ID) (View, error) {
	view, err := s.Get(ctx, id)
	if err != nil {
		return View{}, &CommittedError{Cause: err}
	}
	return view, nil
}
