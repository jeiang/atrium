package integrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/integrations/transport"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
)

const (
	Untested    = "untested"
	OK          = "ok"
	Failing     = "failing"
	Expiring    = "expiring"
	NeedsReauth = "needs_reauth"

	ExpiryWindow = 14 * 24 * time.Hour
	testTimeout  = 15 * time.Second
)

var ErrAuth = errors.New("credentials refused")

type Conn struct {
	URL, Secret string
	HTTP        *http.Client
	DB          *db.DB
	Env         config.Env
}

type Field = settings.Field

type FieldView = settings.FieldView

type Section struct {
	Name, Title, Module string
	Template            string
	Fields              []Field
}

type SectionView struct {
	Name     string      `json:"name"`
	Title    string      `json:"title"`
	Template string      `json:"template"`
	Fields   []FieldView `json:"fields"`
}

type Integration struct {
	Name, Label, Module string
	Enables             string
	URLKey, SecretKey   string
	Help                string
	Fields              []Field
	Test                func(ctx context.Context, c Conn) (string, error)
	Expires             func(ctx context.Context, q db.Querier) (*time.Time, error)
}

type Notifier func(ctx context.Context, d *db.DB, env config.Env, title, body string) error

var (
	mu       sync.Mutex
	registry []Integration
	sections []Section
	notifier Notifier
)

func Register(i Integration) {
	mu.Lock()
	defer mu.Unlock()
	if slices.ContainsFunc(registry, func(r Integration) bool { return r.Name == i.Name }) {
		return
	}
	settings.MustRegister(
		settings.Value(settings.Advanced, settings.Def{Key: transport.CAKey(i.Name), Module: i.Module, Type: settings.String}),
		settings.Value(settings.Advanced, settings.Def{Key: transport.ProxyKey(i.Name), Module: i.Module, Type: settings.String, Check: proxyCheck}),
	)
	registry = append(registry, i)
}

func RegisterSection(s Section) {
	mu.Lock()
	defer mu.Unlock()
	if slices.ContainsFunc(sections, func(r Section) bool { return r.Name == s.Name }) {
		return
	}
	sections = append(sections, s)
}

func AllSections() []Section {
	mu.Lock()
	defer mu.Unlock()
	return slices.Clone(sections)
}

func SectionNamed(name string) (Section, bool) {
	for _, s := range AllSections() {
		if s.Name == name {
			return s, true
		}
	}
	return Section{}, false
}

func LoadSections(ctx context.Context, q db.Querier) ([]SectionView, error) {
	var out []SectionView
	for _, s := range AllSections() {
		fields, err := settings.LoadFields(ctx, q, s.Fields)
		if err != nil {
			return nil, err
		}
		out = append(out, SectionView{Name: s.Name, Title: s.Title, Template: s.Template, Fields: fields})
	}
	return out, nil
}

func SaveSection(ctx context.Context, d *db.DB, name string, values map[string]string) error {
	s, ok := SectionNamed(name)
	if !ok {
		return Unknown(name)
	}
	return settings.SaveFields(ctx, d, s.Fields, values)
}

func proxyCheck(value string) error {
	if _, err := transport.New(transport.Options{Proxy: value}); err != nil {
		return settings.Invalid(err.Error())
	}
	return nil
}

func SetNotifier(n Notifier) {
	mu.Lock()
	defer mu.Unlock()
	notifier = n
}

func All() []Integration {
	mu.Lock()
	defer mu.Unlock()
	out := slices.Clone(registry)
	slices.SortFunc(out, func(a, b Integration) int { return strings.Compare(a.Label, b.Label) })
	return out
}

func Get(name string) (Integration, bool) {
	for _, i := range All() {
		if i.Name == name {
			return i, true
		}
	}
	return Integration{}, false
}

func (i Integration) gate() string {
	if i.Enables != "" {
		return i.Enables
	}
	return i.Module
}

func text(ctx context.Context, q db.Querier, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	found, err := settings.From(ctx).Resolve(ctx, q, key)
	return strings.TrimSpace(found.Value), err
}

type Record struct {
	Name         string      `json:"name"`
	Label        string      `json:"label"`
	Module       string      `json:"module"`
	Help         string      `json:"help"`
	URLKey       string      `json:"url_key"`
	URL          string      `json:"url"`
	URLPinned    bool        `json:"url_pinned"`
	SecretKey    string      `json:"secret_key"`
	SecretSet    bool        `json:"secret_set"`
	SecretPinned bool        `json:"secret_pinned"`
	Fields       []FieldView `json:"fields"`
	State        string      `json:"state"`
	Reason       string      `json:"reason"`
	CheckedAt    *float64    `json:"checked_at"`
	ExpiresAt    *time.Time  `json:"expires_at"`
}

func (r Record) Configured() bool { return r.URLKey == "" && r.SecretSet || r.URL != "" }

func Records(ctx context.Context, q db.Querier) ([]Record, error) {
	var out []Record
	store := secrets.NewIn(settings.From(ctx), nil, q, nil)
	for _, i := range All() {
		r := Record{Name: i.Name, Label: i.Label, Module: i.Module, Help: i.Help, URLKey: i.URLKey, SecretKey: i.SecretKey, State: Untested}
		if i.URLKey != "" {
			found, err := settings.From(ctx).Resolve(ctx, q, i.URLKey)
			if err != nil {
				return nil, err
			}
			def, _ := settings.From(ctx).Lookup(i.URLKey)
			r.URL, r.URLPinned = found.Value, found.Pinned() || !def.Editable()
		}
		if i.SecretKey != "" {
			status, err := store.Status(ctx, i.SecretKey)
			if err != nil {
				return nil, err
			}
			r.SecretSet, r.SecretPinned = status != settings.Unset, status == settings.Pinned
		}
		fields, err := settings.LoadFields(ctx, q, i.Fields)
		if err != nil {
			return nil, err
		}
		r.Fields = fields
		var expires *float64
		err = q.QueryRowContext(ctx, "SELECT state, reason, checked_at, expires_at FROM integration_health WHERE name = ?", i.Name).
			Scan(&r.State, &r.Reason, &r.CheckedAt, &expires)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if expires != nil {
			t := time.Unix(int64(*expires), 0)
			r.ExpiresAt = &t
		}
		out = append(out, r)
	}
	return out, nil
}

func current(ctx context.Context, d *db.DB, q db.Querier, i Integration, env config.Env) (Conn, error) {
	u, err := text(ctx, q, i.URLKey)
	if err != nil {
		return Conn{}, err
	}
	var secret string
	if i.SecretKey != "" {
		if secret, err = secrets.NewIn(settings.From(ctx), nil, q, nil).Value(ctx, i.SecretKey); err != nil {
			return Conn{}, err
		}
	}
	return Conn{URL: strings.TrimRight(u, "/"), Secret: secret, DB: d, Env: env}, nil
}

func run(ctx context.Context, q db.Querier, i Integration, c Conn) (state, reason string, expires *time.Time, err error) {
	ctx, span := obs.Start(ctx, "integration.check", trace.WithAttributes(obs.String(obs.IntegrationName, i.Name)))
	defer func() {
		if err != nil {
			obs.Fail(span, "error")
		} else {
			span.SetAttributes(obs.String(obs.IntegrationState, state))
		}
		span.End()
	}()
	if c.HTTP, err = transport.Configured(ctx, q, i.Name, testTimeout); err != nil {
		var te transport.Error
		if errors.As(err, &te) {
			return Failing, te.Error(), nil, nil
		}
		return "", "", nil, err
	}
	defer c.HTTP.CloseIdleConnections()
	if i.URLKey != "" && c.URL == "" {
		return Failing, "no URL configured", nil, nil
	}
	if i.SecretKey != "" && c.Secret == "" && i.URLKey == "" {
		return NeedsReauth, "no credential configured", nil, nil
	}
	detail, terr := i.Test(ctx, c)
	if terr != nil {
		if errors.Is(terr, ErrAuth) {
			return NeedsReauth, terr.Error(), nil, nil
		}
		return Failing, terr.Error(), nil, nil
	}
	if i.Expires != nil {
		if expires, err = i.Expires(ctx, q); err != nil {
			return "", "", nil, err
		}
		if expires != nil && time.Until(*expires) < ExpiryWindow {
			return Expiring, "credential expires " + expires.Format(time.DateOnly), expires, nil
		}
	}
	return OK, detail, expires, nil
}

func record(ctx context.Context, d *db.DB, env config.Env, i Integration, state, reason string, expires *time.Time, now float64, tested *Conn) error {
	var previous string
	var exp any
	if expires != nil {
		exp = float64(expires.Unix())
	}
	stale := false
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if tested != nil {
			c, err := current(ctx, d, tx, i, env)
			if err != nil {
				return err
			}
			if stale = c.URL != tested.URL || c.Secret != tested.Secret; stale {
				return nil
			}
		}
		err := tx.QueryRowContext(ctx, "SELECT state FROM integration_health WHERE name = ?", i.Name).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO integration_health (name, state, reason, checked_at, expires_at) VALUES (?, ?, ?, ?, ?) "+
			"ON CONFLICT(name) DO UPDATE SET state = excluded.state, reason = excluded.reason, checked_at = excluded.checked_at, expires_at = excluded.expires_at",
			i.Name, state, reason, now, exp)
		return err
	}); err != nil || stale {
		return err
	}
	mu.Lock()
	n := notifier
	mu.Unlock()
	if state == previous || n == nil || previous == "" {
		return nil
	}
	body := i.Label + ": " + strings.ReplaceAll(state, "_", " ")
	if reason != "" {
		body += ": " + reason
	}
	return n(ctx, d, env, "atrium: "+i.Label+" "+strings.ReplaceAll(state, "_", " "), body)
}

func RecordResult(ctx context.Context, d *db.DB, env config.Env, name, label, state, reason string, now float64) error {
	return record(ctx, d, env, Integration{Name: name, Label: label}, state, reason, nil, now, nil)
}

func Check(ctx context.Context, d *db.DB, env config.Env, name string, now float64) (string, error) {
	i, ok := Get(name)
	if !ok {
		return "", Unknown(name)
	}
	c, err := current(ctx, d, d.Read, i, env)
	if err != nil {
		return "", err
	}
	if i.URLKey != "" && c.URL == "" || i.URLKey == "" && c.Secret == "" {
		return Untested, nil
	}
	state, reason, expires, err := run(ctx, d.Read, i, c)
	if err != nil {
		return "", err
	}
	return state, record(ctx, d, env, i, state, reason, expires, now, &c)
}

func CheckAll(ctx context.Context, d *db.DB, env config.Env, now float64) error {
	records, err := Records(ctx, d.Read)
	if err != nil {
		return err
	}
	for _, r := range records {
		if !r.Configured() {
			continue
		}
		if _, err := Check(ctx, d, env, r.Name, now); err != nil {
			return err
		}
	}
	return nil
}

type Candidate struct {
	URL       string
	Secret    string
	HasSecret bool
}

type TestFailed struct{ State, Reason string }

func (e *TestFailed) Error() string { return "test failed (" + e.State + "): " + e.Reason }

func TestAndSave(ctx context.Context, d *db.DB, env config.Env, name string, cand Candidate, by string, now float64) (bool, error) {
	i, ok := Get(name)
	if !ok {
		return false, Unknown(name)
	}
	c, err := current(ctx, d, d.Read, i, env)
	if err != nil {
		return false, err
	}
	urlChanged := false
	if u := strings.TrimRight(strings.TrimSpace(cand.URL), "/"); i.URLKey != "" && u != "" && u != c.URL {
		c.URL, urlChanged = u, true
	}
	if cand.HasSecret {
		c.Secret = cand.Secret
	}
	state, reason, expires, err := run(ctx, d.Read, i, c)
	if err != nil {
		return false, err
	}
	changed := urlChanged || cand.HasSecret
	if !changed {
		if err := record(ctx, d, env, i, state, reason, expires, now, &c); err != nil {
			return false, err
		}
	}
	if state != OK && state != Expiring {
		return false, &TestFailed{State: state, Reason: reason}
	}
	if !changed {
		return false, nil
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if urlChanged {
			if err := settings.From(ctx).Set(ctx, tx, i.URLKey, c.URL); err != nil {
				return err
			}
		}
		if cand.HasSecret {
			return secrets.NewIn(settings.From(ctx), d, tx, d).Set(ctx, i.SecretKey, c.Secret, by)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return true, record(ctx, d, env, i, state, reason, expires, now, &c)
}

func SaveFields(ctx context.Context, d *db.DB, name string, values map[string]string) error {
	i, ok := Get(name)
	if !ok || len(i.Fields) == 0 {
		return Unknown(name)
	}
	return settings.SaveFields(ctx, d, i.Fields, values)
}

func Disconnect(ctx context.Context, d *db.DB, name string) error {
	i, ok := Get(name)
	if !ok {
		return Unknown(name)
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if i.URLKey != "" {
			if err := settings.From(ctx).Unset(ctx, tx, i.URLKey); err != nil {
				return err
			}
		}
		if i.SecretKey != "" {
			store := secrets.NewIn(settings.From(ctx), d, tx, d)
			if status, err := store.Status(ctx, i.SecretKey); err != nil {
				return err
			} else if status == settings.Pinned {
				return &settings.PinnedError{Key: i.SecretKey, Source: "deployment"}
			}
			if err := store.Delete(ctx, i.SecretKey); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM integration_health WHERE name = ?", i.Name)
		return err
	})
}

type Unknown string

func (e Unknown) Error() string { return fmt.Sprintf("unknown integration %q", string(e)) }
