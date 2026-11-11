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
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const defaultTimeout = 15 * time.Second

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

func (s *Store) ReconcileHost(ctx context.Context) error {
	if s.d == nil {
		return errors.New("connections: host reconciliation needs a writable database")
	}
	declared := map[string]bool{}
	for _, id := range sortedKeys(s.host.Connections) {
		declared[id] = true
	}
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
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
		return nil
	})
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
		hostOwns := currentSource == SourceHost
		changed := currentTransport != string(transportJSON) || hostOwns && currentConfig != string(configJSON)
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
				"results_json = CASE WHEN ? THEN '{}' ELSE results_json END, updated_at = ? "+
				"WHERE id = ?",
			labelOr(hc.Label, string(id)), string(transportJSON), revision, changed, changed, changed, changed, changed, s.stamp(), id); err != nil {
			return err
		}
		if hostOwns && changed {
			if _, err := tx.ExecContext(ctx, "UPDATE connections SET config_json = ? WHERE id = ?", string(configJSON), id); err != nil {
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
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO connection_credentials (connection_id, slot, source, file_ref, set_at, set_by) VALUES (?, ?, 'host_file', ?, ?, 'host')",
			id, slot, hc.CredentialFiles[slot], s.stamp()); err != nil {
			return err
		}
	}
	_ = currentRevision
	return nil
}

func (s *Store) List(ctx context.Context) ([]View, error) {
	rows, err := s.q.QueryContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, results_json "+
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
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, results_json "+
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
	)
	if err := sc.Scan(&v.ID, &v.Kind, &v.Label, &v.ConfigSource, &configJSON, &transportJSON, &revision, &enabled, &v.State, &v.Reason,
		&checked, &lastOK, &expires, &reauthAt, &resultsJSON); err != nil {
		return View{}, err
	}
	v.Enabled = enabled != 0
	v.Config = json.RawMessage(configJSON)
	v.revision = revision
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

func (s *Store) open(ctx context.Context, q db.Querier, id ID) (*Lease, error) {
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
	)
	err := q.QueryRowContext(ctx,
		"SELECT id, kind, label, config_source, config_json, transport_json, revision, enabled, state, reason, checked_at, last_ok_at, expires_at, reauth_at, results_json "+
			"FROM connections WHERE id = ?", id).
		Scan(&v.ID, &v.Kind, &v.Label, &v.ConfigSource, &configJSON, &transport, &revision, &enabled, &v.State, &v.Reason,
			&checked, &lastOK, &expires, &reauthAt, &results)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, UnknownID(id)
	}
	if err != nil {
		return nil, err
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
				"UPDATE connections SET revision = revision + 1, state = 'untested', reason = '', checked_at = NULL, last_ok_at = NULL, results_json = '{}' WHERE id = ?", id); err != nil {
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
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	if client, ok := l.shared.clients[name]; ok {
		return client, nil
	}
	timeout := defaultTimeout
	if l.options.Timeout > 0 {
		timeout = time.Duration(l.options.Timeout) * time.Second
	}
	client, err := transport.New(transport.Options{
		Timeout:         timeout,
		CAFile:          l.options.CAFile,
		Proxy:           l.options.Proxy,
		FollowRedirects: true,
	})
	if err != nil {
		return nil, err
	}
	origin := u.Scheme + "://" + u.Host
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme+"://"+req.URL.Host != origin {
			req.Header.Del("Authorization")
		}
		return nil
	}
	l.shared.clients[name] = client
	return client, nil
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

func (l *Lease) Token(_ context.Context, capability string) (string, error) {
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
	l.shared.mu.Lock()
	defer l.shared.mu.Unlock()
	delete(l.shared.tokens, capability)
}

func (l *Lease) Close() { l.closed = true }

func (s *Store) Test(ctx context.Context, id ID) (View, error) {
	lease, err := s.open(ctx, s.q, id)
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
	return s.record(ctx, id, lease.revision, results)
}

func (s *Store) record(ctx context.Context, id ID, revision int64, results []Result) (View, error) {
	state, reason := aggregate(results, nil)
	raw, err := json.Marshal(results)
	if err != nil {
		return View{}, err
	}
	now := s.stamp()
	var lastOK, reauthAt any
	if state == OK {
		lastOK = now
	}
	if state == NeedsReauth {
		reauthAt = now
	}
	res, err := s.d.Write.ExecContext(ctx,
		"UPDATE connections SET state = ?, reason = ?, checked_at = ?, last_ok_at = COALESCE(?, last_ok_at), reauth_at = COALESCE(?, reauth_at), results_json = ?, updated_at = ? "+
			"WHERE id = ? AND revision = ?",
		state, reason, now, lastOK, reauthAt, string(raw), now, id, revision)
	if err != nil {
		return View{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return View{}, err
	}
	if n == 0 {
		return s.Get(ctx, id)
	}
	return s.Get(ctx, id)
}

func (s *Store) TestAndSave(ctx context.Context, id ID, cand Candidate) (View, error) {
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
	lease := s.candidateLease(current, cand)
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
	if err := s.save(ctx, id, current, cand, results, state, reason); err != nil {
		return View{}, err
	}
	return s.Get(ctx, id)
}

func (s *Store) save(ctx context.Context, id ID, current View, cand Candidate, results []Result, state State, reason string) error {
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
	return s.d.WithTx(ctx, func(tx *sql.Tx) error {
		var revision int64
		if err := tx.QueryRowContext(ctx, "SELECT revision FROM connections WHERE id = ?", id).Scan(&revision); err != nil {
			return err
		}
		if revision != current.revisionValue() {
			return &Conflict{ID: id, Revision: current.revisionValue()}
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE connections SET label = ?, config_json = ?, revision = revision + 1, state = ?, reason = ?, checked_at = ?, "+
				"last_ok_at = CASE WHEN ? = 'ok' THEN ? ELSE last_ok_at END, results_json = ?, updated_at = ? WHERE id = ?",
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
		return nil
	})
}

func (s *Store) Disconnect(ctx context.Context, id ID) error {
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
				"last_ok_at = NULL, expires_at = NULL, reauth_at = NULL, results_json = '{}', revision = revision + 1, updated_at = ? WHERE id = ?",
			now, id); err != nil {
			return err
		}
		old := string(current.Config)
		next := "{}"
		return audit.Record(ctx, tx, audit.Entry{Key: "connection." + string(id), Old: &old, New: &next})
	})
}

func (s *Store) Observe(ctx context.Context, id ID, revision int64, result Result) error {
	var (
		currentRevision int64
		resultsJSON     string
	)
	if err := s.d.Read.QueryRowContext(ctx, "SELECT revision, results_json FROM connections WHERE id = ?", id).
		Scan(&currentRevision, &resultsJSON); err != nil {
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
	state, reason := aggregate(results, nil)
	raw, err := json.Marshal(results)
	if err != nil {
		return err
	}
	now := s.stamp()
	_, err = s.d.Write.ExecContext(ctx,
		"UPDATE connections SET state = ?, reason = ?, checked_at = ?, results_json = ?, updated_at = ? WHERE id = ? AND revision = ?",
		state, reason, now, string(raw), now, id, revision)
	return err
}

func (s *Store) candidateLease(current View, cand Candidate) *Lease {
	caps := make([]Capability, 0, len(cand.Capabilities))
	caps = append(caps, cand.Capabilities...)
	creds := map[string]credential{}
	for _, c := range current.Credentials {
		creds[c.Slot] = credential{source: c.Source, fileRef: c.FileRef, expires: c.ExpiresAt}
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
	return lease
}

func validateCandidate(def Definition, cand Candidate, current View) error {
	if cand.Config == nil {
		cand.Config = json.RawMessage("{}")
	}
	if !json.Valid(cand.Config) {
		return errors.New("connections: config must be valid JSON")
	}
	if current.ConfigSource == SourceHost {
		var want, got map[string]string
		_ = json.Unmarshal(current.Config, &want)
		_ = json.Unmarshal(cand.Config, &got)
		if !sameMap(want, got) {
			return errors.New("connections: the host owns this connection's identity and endpoints")
		}
	}
	if def.Validate != nil {
		if err := def.Validate(cand); err != nil {
			return err
		}
	}
	for _, c := range cand.Credentials {
		if c.HostFileRef != "" {
			return errors.New("connections: a host file credential is only accepted from host configuration")
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
