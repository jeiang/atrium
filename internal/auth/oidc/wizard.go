package oidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/settings/audit"
)

const (
	testPassTTL = 15 * time.Minute

	EntraIssuerPrefix = "https://login.microsoftonline.com/"
	EntraIssuerSuffix = "/v2.0"
)

var (
	ErrNoPassedTest = errors.New("oidc: no sign-in test has passed")
	ErrStaleTest    = errors.New("oidc: the passed sign-in test is for a different configuration")
	ErrNeedsCipher  = errors.New("oidc: the client secret can only be stored in an encrypted database")
)

type FieldError struct {
	Key string
	Err error
}

func (e *FieldError) Error() string { return e.Key + ": " + e.Err.Error() }

func (e *FieldError) Unwrap() error { return e.Err }

type Form struct {
	Preset        string
	Issuer        string
	ClientID      string
	ClientSecret  string
	Scopes        string
	GroupsClaim   string
	AccessGroup   string
	AdminGroup    string
	AccessEmails  string
	AdminEmails   string
	AccessDomains string
	AdminDomains  string
}

type Draft struct {
	form        Form
	cfg         Config
	vals        values
	secret      string
	secretGiven bool
	changed     map[string]string
	fingerprint string
}

type View struct {
	Preset       Preset
	PresetPinned bool
	Values       map[string]string
	Pinned       map[string]bool
	SecretSet    bool
	SecretPinned bool
	Active       bool
	Public       string
}

func (s *Service) pinned(key string) (bool, error) {
	_, ok, err := settings.Default.Pinned(key)
	return ok, err
}

func (s *Service) View(ctx context.Context, presetName string) (View, error) {
	vals, problems, err := resolved(ctx, s.d.Read)
	if err != nil {
		return View{}, err
	}
	if len(problems) > 0 {
		return View{}, errors.New(strings.Join(problems, "; "))
	}
	v := View{Values: vals, Pinned: map[string]bool{}}
	for _, key := range valueKeys {
		pinned, err := s.pinned(key)
		if err != nil {
			return View{}, err
		}
		v.Pinned[key] = pinned
	}
	v.PresetPinned = v.Pinned[KeyPreset]
	active, _ := presetNamed(vals[KeyPreset])
	name := presetName
	if name == "" || v.PresetPinned {
		name = active.Name
	}
	preset, ok := presetNamed(name)
	if !ok {
		preset = active
	}
	v.Preset = preset
	if preset.Name != active.Name {
		for key := range vals {
			if !v.Pinned[key] && slices.Contains(wizardKeys, key) {
				vals[key] = ""
			}
		}
		vals[KeyAccessGroup], vals[KeyAdminGroup] = DefaultAccessGroup, DefaultAdminGroup
		if preset.Placeholder != "" {
			vals[KeyAccessGroup], vals[KeyAdminGroup] = "", ""
		}
	} else if preset.Placeholder != "" && vals[KeyAccessGroup] == DefaultAccessGroup && vals[KeyAdminGroup] == DefaultAdminGroup {
		vals[KeyAccessGroup], vals[KeyAdminGroup] = "", ""
	}
	if preset.Name == PresetEntra && strings.HasPrefix(vals[KeyIssuer], EntraIssuerPrefix) && strings.HasSuffix(vals[KeyIssuer], EntraIssuerSuffix) {
		vals[KeyIssuer] = strings.TrimSuffix(strings.TrimPrefix(vals[KeyIssuer], EntraIssuerPrefix), EntraIssuerSuffix)
	}
	if preset.Issuer != "" {
		vals[KeyIssuer] = preset.Issuer
	}
	if len(strings.TrimSpace(vals[KeyScopes])) == 0 {
		vals[KeyScopes] = strings.Join(preset.Scopes, " ")
	}
	if vals[KeyGroupsClaim] == "" {
		vals[KeyGroupsClaim] = preset.GroupsClaim
	}
	secret, err := secrets.Credential(ctx, s.d.Read, secrets.OIDCClientSecret)
	var empty settings.EmptyValue
	if errors.As(err, &empty) {
		secret, err = "", nil
	}
	if err != nil {
		return View{}, err
	}
	secretPinned, err := s.pinned(secrets.OIDCClientSecret)
	if err != nil {
		return View{}, err
	}
	v.SecretSet, v.SecretPinned = secret != "" || secretPinned, secretPinned
	_, activeErr := assemble(vals, strings.TrimSpace(secret), s.env, s.d.Encrypted(), nil)
	v.Active = activeErr == nil
	v.Public = config.PublicURL(s.env)
	return v, nil
}

type Field struct {
	Key      string
	ID       string
	Label    string
	Type     string
	Group    string
	Required bool
	Secret   bool
	Show     func(Preset) bool
}

var Fields = []Field{
	{Key: KeyIssuer, ID: "oidc-issuer", Type: "url", Group: "primary", Required: true, Show: func(p Preset) bool { return p.Issuer == "" }},
	{Key: KeyClientID, ID: "oidc-client-id", Label: "Client ID", Type: "text", Group: "primary", Required: true},
	{Key: secrets.OIDCClientSecret, ID: "oidc-client-secret", Label: "Client secret", Type: "password", Group: "primary", Secret: true},
	{Key: KeyAccessGroup, ID: "oidc-access-group", Label: "Access group", Type: "text", Group: "groups", Required: true, Show: func(p Preset) bool { return p.Groups }},
	{Key: KeyAdminGroup, ID: "oidc-admin-group", Label: "Admin group", Type: "text", Group: "groups", Required: true, Show: func(p Preset) bool { return p.Groups }},
	{Key: KeyAccessEmails, ID: "oidc-access-emails", Label: "Access emails", Type: "text", Group: "lists", Show: func(p Preset) bool { return p.Lists }},
	{Key: KeyAdminEmails, ID: "oidc-admin-emails", Label: "Admin emails", Type: "text", Group: "lists", Show: func(p Preset) bool { return p.Lists }},
	{Key: KeyAccessDomains, ID: "oidc-access-domains", Label: "Access domains", Type: "text", Group: "lists", Show: func(p Preset) bool { return p.Lists }},
	{Key: KeyAdminDomains, ID: "oidc-admin-domains", Label: "Admin domains", Type: "text", Group: "lists", Show: func(p Preset) bool { return p.Lists }},
	{Key: KeyGroupsClaim, ID: "oidc-groups-claim", Label: "Groups claim", Type: "text", Group: "advanced", Show: func(p Preset) bool { return p.Groups }},
	{Key: KeyScopes, ID: "oidc-scopes", Label: "Scopes", Type: "text", Group: "advanced"},
}

func fieldKeys() []string {
	var keys []string
	for _, f := range Fields {
		if !f.Secret {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

var wizardKeys = fieldKeys()

func init() {
	settings.Default.Claim("/settings/auth", append([]string{KeyPreset, secrets.OIDCClientSecret}, wizardKeys...)...)
}

func (f Form) shown(preset Preset) map[string]string {
	out := map[string]string{KeyClientID: f.ClientID, KeyScopes: f.Scopes}
	if preset.Issuer == "" {
		issuer := f.Issuer
		if preset.Name == PresetEntra {
			issuer = strings.TrimSpace(f.Issuer)
			if issuer != "" && !strings.HasPrefix(issuer, EntraIssuerPrefix) {
				issuer = EntraIssuerPrefix + issuer + EntraIssuerSuffix
			}
		}
		out[KeyIssuer] = issuer
	}
	if preset.Groups {
		out[KeyGroupsClaim] = f.GroupsClaim
		out[KeyAccessGroup], out[KeyAdminGroup] = f.AccessGroup, f.AdminGroup
	}
	if preset.Lists {
		out[KeyAccessEmails], out[KeyAdminEmails] = f.AccessEmails, f.AdminEmails
		out[KeyAccessDomains], out[KeyAdminDomains] = f.AccessDomains, f.AdminDomains
	}
	return out
}

func (s *Service) Draft(ctx context.Context, f Form) (Draft, error) {
	d, err := s.draftFrom(ctx, s.d.Read, f)
	d.form = f
	return d, err
}

func (s *Service) draftFrom(ctx context.Context, q db.Querier, f Form) (Draft, error) {
	active, problems, err := resolved(ctx, q)
	if err != nil {
		return Draft{}, err
	}
	if len(problems) > 0 {
		return Draft{}, errors.New(strings.Join(problems, "; "))
	}
	preset, ok := presetNamed(f.Preset)
	presetPinned, err := s.pinned(KeyPreset)
	if err != nil {
		return Draft{}, err
	}
	if presetPinned {
		preset, ok = presetNamed(active[KeyPreset])
	}
	if !ok {
		return Draft{}, &FieldError{KeyPreset, settings.Invalid("is not a known provider")}
	}
	d := Draft{vals: values{}, changed: map[string]string{}}
	for k, v := range active {
		d.vals[k] = v
	}
	shown := f.shown(preset)
	next := map[string]string{KeyPreset: preset.Name}
	for _, key := range wizardKeys {
		if v, ok := shown[key]; ok {
			next[key] = strings.TrimSpace(v)
			continue
		}
		next[key] = ""
		if key == KeyAccessGroup {
			next[key] = DefaultAccessGroup
		}
		if key == KeyAdminGroup {
			next[key] = DefaultAdminGroup
		}
	}
	if strings.Join(list(next[KeyScopes]), " ") == strings.Join(preset.Scopes, " ") {
		next[KeyScopes] = ""
	}
	if next[KeyGroupsClaim] == preset.GroupsClaim {
		next[KeyGroupsClaim] = ""
	}
	for _, key := range append([]string{KeyPreset}, wizardKeys...) {
		value := next[key]
		pinned, err := s.pinned(key)
		if err != nil {
			return Draft{}, err
		}
		if pinned {
			continue
		}
		if value != "" || key == KeyAccessGroup || key == KeyAdminGroup {
			if err := settings.Default.Validate(key, value); err != nil {
				return Draft{}, &FieldError{key, err}
			}
		}
		d.vals[key] = value
		if value != active[key] {
			d.changed[key] = value
		}
	}
	stored, err := secrets.Credential(ctx, q, secrets.OIDCClientSecret)
	if err != nil {
		return Draft{}, err
	}
	d.secret = strings.TrimSpace(stored)
	secretPinned, err := s.pinned(secrets.OIDCClientSecret)
	if err != nil {
		return Draft{}, err
	}
	if given := strings.TrimSpace(f.ClientSecret); given != "" && !secretPinned {
		if !s.d.Encrypted() {
			return Draft{}, ErrNeedsCipher
		}
		d.secret, d.secretGiven = given, true
	}
	d.cfg, err = assemble(d.vals, d.secret, s.env, s.d.Encrypted(), nil)
	if err != nil {
		return Draft{}, err
	}
	d.fingerprint = fingerprint(d)
	return d, nil
}

func fingerprint(d Draft) string {
	h := sha256.New()
	keys := make([]string, 0, len(d.vals))
	for k := range d.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%s=%q\n", k, d.vals[k])
	}
	_, _ = fmt.Fprintf(h, "secret=%q\n", d.secret)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

type TestStatus string

const (
	TestIdle    TestStatus = "idle"
	TestPending TestStatus = "pending"
	TestPassed  TestStatus = "passed"
	TestFailed  TestStatus = "failed"
)

type TestResult struct {
	Status      TestStatus
	Reason      string
	HTTP        int
	Groups      []string
	Fingerprint string
	Seq         int
}

func testFailure(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNoPKCE):
		return 400, "The provider does not support PKCE S256."
	case errors.Is(err, ErrRejected):
		return 400, "The provider rejected the client."
	case errors.Is(err, ErrToken), errors.Is(err, ErrLogin):
		return 400, "The provider response was not valid."
	case errors.Is(err, ErrLoginTimeout):
		return 400, "Sign-in timed out."
	case errors.Is(err, ErrProvider):
		return 502, "The issuer did not respond."
	}
	return 500, "The test could not finish."
}

type storedDraft struct {
	Form        Form   `json:"form"`
	Secret      string `json:"secret"`
	SecretGiven bool   `json:"secret_given"`
}

func (s *Service) sealDraft(ctx context.Context, d *Draft) (string, error) {
	key, err := secrets.New(s.d, s.d).Generate(ctx, secrets.ActionLinkKey)
	if err != nil {
		return "", err
	}
	form := d.form
	form.ClientSecret = ""
	stored := storedDraft{Form: form}
	if d.secretGiven {
		stored.Secret, stored.SecretGiven = d.secret, true
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return "", err
	}
	return newSealer(key, s.d.Encrypted()).seal(string(raw)), nil
}

func (s *Service) openDraft(ctx context.Context, raw string) (Form, error) {
	key, err := secrets.New(s.d, s.d).Generate(ctx, secrets.ActionLinkKey)
	if err != nil {
		return Form{}, err
	}
	var stored storedDraft
	if err := json.Unmarshal([]byte(newSealer(key, s.d.Encrypted()).open(raw)), &stored); err != nil {
		return Form{}, err
	}
	if stored.SecretGiven {
		stored.Form.ClientSecret = stored.Secret
	}
	return stored.Form, nil
}

func (s *Service) insertTest(ctx context.Context, stateHash, bindingHash, nonce, verifier string, d *Draft, status TestStatus, reason string, httpStatus int, groups []string, passedAt any) (int64, error) {
	var draft, fingerprint string
	if d != nil {
		var err error
		draft, err = s.sealDraft(ctx, d)
		if err != nil {
			return 0, err
		}
		fingerprint = d.fingerprint
	}
	rawGroups, err := json.Marshal(groups)
	if err != nil {
		return 0, err
	}
	res, err := s.d.Write.ExecContext(ctx, "INSERT INTO auth_logins (state_hash, binding_hash, nonce, verifier, return_to, created_at, purpose, status, reason, http, groups, fingerprint, draft, passed_at) VALUES (?, ?, ?, ?, '', ?, 'test', ?, ?, ?, ?, ?, ?, ?)",
		stateHash, bindingHash, nonce, verifier, s.unix(), string(status), reason, httpStatus, string(rawGroups), fingerprint, draft, passedAt)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Service) failTestRow(ctx context.Context, seq int64, status int, reason string) {
	if _, err := s.d.Write.ExecContext(ctx, "UPDATE auth_logins SET status = 'failed', reason = ?, http = ?, passed_at = NULL WHERE rowid = ? AND purpose = 'test'", reason, status, seq); err != nil {
		log.Error("test sign-in result could not be stored", "err", err.Error())
	}
}

func (s *Service) testFailure(ctx context.Context, d *Draft, status int, reason string) TestResult {
	seq, err := s.insertTest(ctx, digest(randomToken()), "", "", "", d, TestFailed, reason, status, nil, nil)
	if err != nil {
		log.Error("test sign-in result could not be stored", "err", err.Error())
		return TestResult{Status: TestFailed, Reason: reason, HTTP: status}
	}
	fingerprint := ""
	if d != nil {
		fingerprint = d.fingerprint
	}
	return TestResult{Status: TestFailed, Reason: reason, HTTP: status, Fingerprint: fingerprint, Seq: int(seq)}
}

func (s *Service) BeginTest(ctx context.Context, d Draft) (Login, error) {
	_, _ = s.d.Write.ExecContext(ctx, "DELETE FROM auth_logins WHERE purpose = 'test' AND created_at < ?", s.unix()-loginTTL.Seconds())
	p, err := s.provider(ctx, d.cfg.Issuer)
	if err != nil {
		status, reason := testFailure(err)
		s.testFailure(ctx, &d, status, reason)
		return Login{}, err
	}
	state, binding, nonce, verifier := randomToken(), randomToken(), randomToken(), randomToken()
	target, err := s.authorizeURL(d.cfg, p, state, nonce, verifier)
	if err != nil {
		status, reason := testFailure(err)
		s.testFailure(ctx, &d, status, reason)
		return Login{}, err
	}
	sl := newSealer(d.cfg.ClientSecret, s.d.Encrypted())
	if _, err := s.insertTest(ctx, digest(state), digest(binding), sl.sealPending(nonce), sl.sealPending(verifier), &d, TestPending, "", 0, nil, nil); err != nil {
		return Login{}, err
	}
	return Login{URL: target, Binding: binding}, nil
}

func (s *Service) IsTestState(state string) bool {
	if state == "" {
		return false
	}
	var one int
	err := s.d.Read.QueryRowContext(context.Background(), "SELECT 1 FROM auth_logins WHERE state_hash = ? AND purpose = 'test' AND status = 'pending'", digest(state)).Scan(&one)
	return err == nil
}

func (s *Service) TestResult() TestResult {
	res, _, created, _, err := s.latestTest(context.Background())
	if errors.Is(err, sql.ErrNoRows) {
		return TestResult{Status: TestIdle}
	}
	if err != nil {
		log.Error("test sign-in result could not be read", "err", err.Error())
		return TestResult{Status: TestIdle}
	}
	if res.Status == TestPending && s.unix()-created > loginTTL.Seconds() {
		s.failTestRow(context.Background(), int64(res.Seq), 400, "Sign-in timed out.")
		res.Status, res.Reason, res.HTTP = TestFailed, "Sign-in timed out.", 400
	}
	return res
}

func (s *Service) latestTest(ctx context.Context) (TestResult, string, float64, sql.NullFloat64, error) {
	var res TestResult
	var status, groups, draft string
	var created float64
	var passed sql.NullFloat64
	err := s.d.Read.QueryRowContext(ctx, "SELECT rowid, status, reason, http, groups, fingerprint, draft, created_at, passed_at FROM auth_logins WHERE purpose = 'test' ORDER BY rowid DESC LIMIT 1").
		Scan(&res.Seq, &status, &res.Reason, &res.HTTP, &groups, &res.Fingerprint, &draft, &created, &passed)
	if err != nil {
		return res, "", 0, passed, err
	}
	res.Status = TestStatus(status)
	if groups != "" {
		_ = json.Unmarshal([]byte(groups), &res.Groups)
	}
	return res, draft, created, passed, nil
}

func (s *Service) RejectTest(status int, reason string) TestResult {
	return s.testFailure(context.Background(), nil, status, reason)
}

func (s *Service) ResetTest() {
	if _, err := s.d.Write.ExecContext(context.Background(), "DELETE FROM auth_logins WHERE purpose = 'test'"); err != nil {
		log.Error("test sign-in rows could not be reset", "err", err.Error())
	}
}

func (s *Service) CompleteTest(ctx context.Context, state, code, providerError, binding string) TestResult {
	if state == "" || binding == "" {
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid."}
	}
	var seq int64
	var bindingHash, nonce, verifier, draft, fingerprint, status string
	var created float64
	err := s.d.Read.QueryRowContext(ctx, "SELECT rowid, binding_hash, nonce, verifier, created_at, status, fingerprint, draft FROM auth_logins WHERE state_hash = ? AND purpose = 'test'", digest(state)).
		Scan(&seq, &bindingHash, &nonce, &verifier, &created, &status, &fingerprint, &draft)
	if errors.Is(err, sql.ErrNoRows) {
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid."}
	}
	if err != nil {
		log.Error("test sign-in state could not be read", "err", err.Error())
		return TestResult{Status: TestFailed, HTTP: 500, Reason: "The test could not finish."}
	}
	if status != string(TestPending) {
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid."}
	}
	if subtle.ConstantTimeCompare([]byte(bindingHash), []byte(digest(binding))) != 1 {
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid."}
	}
	if s.unix()-created > loginTTL.Seconds() {
		s.failTestRow(ctx, seq, 400, "Sign-in timed out.")
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "Sign-in timed out.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	if providerError != "" {
		log.Warn("test sign-in refused by the provider", "error", sanitize(providerError))
		s.failTestRow(ctx, seq, 400, "The provider rejected the sign-in.")
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider rejected the sign-in.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	if code == "" {
		s.failTestRow(ctx, seq, 400, "The provider response was not valid.")
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	form, err := s.openDraft(ctx, draft)
	if err != nil {
		s.failTestRow(ctx, seq, 500, "The test could not finish.")
		return TestResult{Status: TestFailed, HTTP: 500, Reason: "The test could not finish.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	d, err := s.draftFrom(ctx, s.d.Read, form)
	if err != nil {
		s.failTestRow(ctx, seq, 500, "The test could not finish.")
		return TestResult{Status: TestFailed, HTTP: 500, Reason: "The test could not finish.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	if d.fingerprint != fingerprint {
		s.failTestRow(ctx, seq, 400, "The settings changed during the test.")
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The settings changed during the test.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	sl := newSealer(d.cfg.ClientSecret, s.d.Encrypted())
	nonce, verifier = sl.openPending(nonce), sl.openPending(verifier)
	p, err := s.provider(ctx, d.cfg.Issuer)
	var id identity
	if err == nil {
		_, id, err = s.exchange(ctx, d.cfg, p, code, verifier, nonce)
	}
	if err != nil {
		log.Warn("test sign-in failed", "reason", describe(err))
		status, reason := testFailure(err)
		s.failTestRow(ctx, seq, status, reason)
		return TestResult{Status: TestFailed, HTTP: status, Reason: reason, Fingerprint: fingerprint, Seq: int(seq)}
	}
	access, admin := d.cfg.decide(id)
	switch {
	case access && admin:
	case d.cfg.GroupsClaim != "" && !id.HasGroups && !access:
		s.failTestRow(ctx, seq, 403, "No `"+d.cfg.GroupsClaim+"` claim in the response.")
		return TestResult{Status: TestFailed, HTTP: 403, Reason: "No `" + d.cfg.GroupsClaim + "` claim in the response.", Fingerprint: fingerprint, Seq: int(seq)}
	case !access:
		reason := "This account does not have access."
		if d.cfg.GroupsClaim != "" {
			reason = "This account is not in `" + d.cfg.AccessGroup + "`."
		}
		s.failTestRow(ctx, seq, 403, reason)
		return TestResult{Status: TestFailed, HTTP: 403, Reason: reason, Fingerprint: fingerprint, Seq: int(seq)}
	default:
		reason := "This account is not an administrator."
		if d.cfg.GroupsClaim != "" {
			reason = "This account is not in `" + d.cfg.AdminGroup + "`."
		}
		s.failTestRow(ctx, seq, 403, reason)
		return TestResult{Status: TestFailed, HTTP: 403, Reason: reason, Fingerprint: fingerprint, Seq: int(seq)}
	}
	var seen []string
	for _, g := range []string{d.cfg.AccessGroup, d.cfg.AdminGroup} {
		if slices.Contains(id.Groups, g) && !slices.Contains(seen, g) {
			seen = append(seen, g)
		}
	}
	rawGroups, _ := json.Marshal(seen)
	if _, err := s.d.Write.ExecContext(ctx, "UPDATE auth_logins SET status = 'passed', reason = '', http = 200, groups = ?, passed_at = ? WHERE rowid = ? AND purpose = 'test'", string(rawGroups), s.unix(), seq); err != nil {
		log.Error("test sign-in result could not be stored", "err", err.Error())
		return TestResult{Status: TestFailed, HTTP: 500, Reason: "The test could not finish.", Fingerprint: fingerprint, Seq: int(seq)}
	}
	return TestResult{Status: TestPassed, HTTP: 200, Groups: seen, Fingerprint: fingerprint, Seq: int(seq)}
}

func (s *Service) Activate(ctx context.Context, fingerprint string) error {
	res, draft, _, passed, err := s.latestTest(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoPassedTest
	}
	if err != nil {
		return err
	}
	if res.Status != TestPassed || !passed.Valid || s.unix()-passed.Float64 > testPassTTL.Seconds() {
		return ErrNoPassedTest
	}
	if fingerprint == "" || fingerprint != res.Fingerprint {
		return ErrStaleTest
	}
	form, err := s.openDraft(ctx, draft)
	if err != nil {
		return ErrStaleTest
	}
	d, err := s.draftFrom(ctx, s.d.Read, form)
	if err != nil || d.fingerprint != res.Fingerprint {
		return ErrStaleTest
	}
	d.form = form
	ctx = audit.With(ctx, "operator", audit.UI)
	err = s.d.WithTx(ctx, func(tx *sql.Tx) error {
		current, err := s.draftFrom(ctx, tx, d.form)
		if err != nil || current.fingerprint != d.fingerprint {
			return ErrStaleTest
		}
		d = current
		keys := make([]string, 0, len(d.changed))
		for k := range d.changed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			var err error
			if d.changed[key] == "" && key != KeyAccessGroup && key != KeyAdminGroup {
				err = settings.Default.Unset(ctx, tx, key)
			} else {
				err = settings.Default.Set(ctx, tx, key, d.changed[key])
			}
			if err != nil {
				return err
			}
		}
		if d.secretGiven {
			return secrets.NewIn(s.d, tx, s.d).Set(ctx, secrets.OIDCClientSecret, d.secret, "ui")
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.ResetTest()
	return nil
}
