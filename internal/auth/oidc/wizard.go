package oidc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
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
	cfg         Config
	vals        values
	secret      string
	secretGiven bool
	changed     map[string]string
	fingerprint string
}

func (d Draft) Config() Config { return d.cfg }

func (d Draft) Fingerprint() string { return d.fingerprint }

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

func (s *Service) pinned(key string) bool {
	_, ok, _ := settings.Default.Pinned(key)
	return ok
}

func (s *Service) View(ctx context.Context, presetName string) (View, error) {
	vals, problems := resolved(ctx, s.d.Read)
	if len(problems) > 0 {
		return View{}, errors.New(strings.Join(problems, "; "))
	}
	v := View{Values: vals, Pinned: map[string]bool{}}
	for _, key := range valueKeys {
		v.Pinned[key] = s.pinned(key)
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
	if preset.Name == PresetEntra {
		tenant := strings.TrimSuffix(strings.TrimPrefix(vals[KeyIssuer], EntraIssuerPrefix), EntraIssuerSuffix)
		if tenant == vals[KeyIssuer] {
			tenant = ""
		}
		vals[KeyIssuer] = tenant
	}
	if preset.Issuer != "" {
		vals[KeyIssuer] = preset.Issuer
	}
	if len(py.Strip(vals[KeyScopes])) == 0 {
		vals[KeyScopes] = strings.Join(preset.Scopes, " ")
	}
	if vals[KeyGroupsClaim] == "" {
		vals[KeyGroupsClaim] = preset.GroupsClaim
	}
	status, err := secrets.New(s.d, s.d).IsSet(ctx, secrets.OIDCClientSecret)
	if err != nil {
		return View{}, err
	}
	v.SecretSet, v.SecretPinned = status != secrets.Unset, status == secrets.Pinned
	v.Active = s.Check(ctx) == nil
	v.Public = config.PublicURL(s.env)
	return v, nil
}

var wizardKeys = []string{
	KeyIssuer, KeyClientID, KeyScopes, KeyGroupsClaim, KeyAccessGroup, KeyAdminGroup,
	KeyAccessEmails, KeyAdminEmails, KeyAccessDomains, KeyAdminDomains,
}

func (f Form) shown(preset Preset) map[string]string {
	out := map[string]string{KeyClientID: f.ClientID, KeyScopes: f.Scopes}
	if preset.Issuer == "" {
		issuer := f.Issuer
		if preset.Name == PresetEntra {
			issuer = EntraIssuerPrefix + py.Strip(f.Issuer) + EntraIssuerSuffix
			if py.Strip(f.Issuer) == "" {
				issuer = ""
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
	active, problems := resolved(ctx, s.d.Read)
	if len(problems) > 0 {
		return Draft{}, errors.New(strings.Join(problems, "; "))
	}
	preset, ok := presetNamed(f.Preset)
	if s.pinned(KeyPreset) {
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
			next[key] = py.Strip(v)
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
	for key, value := range next {
		if s.pinned(key) {
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
	stored, err := secrets.Credential(ctx, s.d.Read, secrets.OIDCClientSecret)
	if err != nil {
		return Draft{}, err
	}
	d.secret = py.Strip(stored)
	if given := py.Strip(f.ClientSecret); given != "" && !s.pinned(secrets.OIDCClientSecret) {
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

type testRun struct {
	seq      int
	draft    Draft
	state    string
	binding  string
	nonce    string
	verifier string
	created  time.Time
	passedAt time.Time
	result   TestResult
}

func (s *Service) failTest(run *testRun, status int, reason string) TestResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	run.state = ""
	run.result = TestResult{Status: TestFailed, Reason: reason, HTTP: status, Fingerprint: run.draft.fingerprint}
	return run.result
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

func (s *Service) BeginTest(ctx context.Context, d Draft) (Login, error) {
	run := &testRun{draft: d, created: s.now()}
	s.mu.Lock()
	s.testSeq++
	run.seq = s.testSeq
	s.test = run
	s.mu.Unlock()
	p, err := s.provider(ctx, d.cfg.Issuer)
	if err != nil {
		status, reason := testFailure(err)
		s.failTest(run, status, reason)
		return Login{}, err
	}
	state, binding := randomToken(), randomToken()
	run.nonce, run.verifier = randomToken(), randomToken()
	target, err := s.authorizeURL(d.cfg, p, state, run.nonce, run.verifier)
	if err != nil {
		status, reason := testFailure(err)
		s.failTest(run, status, reason)
		return Login{}, err
	}
	s.mu.Lock()
	run.state, run.binding = digest(state), digest(binding)
	run.result = TestResult{Status: TestPending, Fingerprint: d.fingerprint}
	s.mu.Unlock()
	return Login{URL: target, Binding: binding}, nil
}

func (s *Service) IsTestState(state string) bool {
	if state == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.test != nil && s.test.state != "" && s.test.state == digest(state)
}

func (s *Service) TestResult() TestResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.test == nil {
		return TestResult{Status: TestIdle, Seq: s.testSeq}
	}
	res := s.test.result
	res.Seq = s.test.seq
	return res
}

func (s *Service) RejectTest(status int, reason string) TestResult {
	s.mu.Lock()
	s.testSeq++
	s.test = &testRun{seq: s.testSeq, result: TestResult{Status: TestFailed, Reason: reason, HTTP: status}}
	s.mu.Unlock()
	return s.TestResult()
}

func (s *Service) ResetTest() {
	s.mu.Lock()
	s.test = nil
	s.mu.Unlock()
}

func (s *Service) CompleteTest(ctx context.Context, state, code, providerError, binding string) TestResult {
	s.mu.Lock()
	run := s.test
	if run == nil || run.state == "" || run.state != digest(state) {
		s.mu.Unlock()
		return TestResult{Status: TestFailed, HTTP: 400, Reason: "The provider response was not valid."}
	}
	run.state = ""
	matched := run.binding == digest(binding) && binding != ""
	expired := s.now().Sub(run.created) > loginTTL
	s.mu.Unlock()
	switch {
	case !matched:
		return s.failTest(run, 400, "The provider response was not valid.")
	case expired:
		return s.failTest(run, 400, "Sign-in timed out.")
	case providerError != "":
		log.Warn("test sign-in refused by the provider", "error", sanitize(providerError))
		return s.failTest(run, 400, "The provider rejected the sign-in.")
	case code == "":
		return s.failTest(run, 400, "The provider response was not valid.")
	}
	cfg := run.draft.cfg
	p, err := s.provider(ctx, cfg.Issuer)
	var id identity
	if err == nil {
		_, id, err = s.exchange(ctx, cfg, p, code, run.verifier, run.nonce)
	}
	if err != nil {
		log.Warn("test sign-in failed", "reason", describe(err))
		status, reason := testFailure(err)
		return s.failTest(run, status, reason)
	}
	access, admin := cfg.decide(id)
	switch {
	case access && admin:
	case cfg.GroupsClaim != "" && !id.HasGroups && !access:
		return s.failTest(run, 403, "No `"+cfg.GroupsClaim+"` claim in the response.")
	case !access:
		if cfg.GroupsClaim != "" {
			return s.failTest(run, 403, "This account is not in `"+cfg.AccessGroup+"`.")
		}
		return s.failTest(run, 403, "This account does not have access.")
	default:
		if cfg.GroupsClaim != "" {
			return s.failTest(run, 403, "This account is not in `"+cfg.AdminGroup+"`.")
		}
		return s.failTest(run, 403, "This account is not an administrator.")
	}
	var seen []string
	for _, g := range []string{cfg.AccessGroup, cfg.AdminGroup} {
		if slices.Contains(id.Groups, g) && !slices.Contains(seen, g) {
			seen = append(seen, g)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run.passedAt = s.now()
	run.result = TestResult{Status: TestPassed, HTTP: 200, Groups: seen, Fingerprint: run.draft.fingerprint}
	return run.result
}

func (s *Service) Activate(ctx context.Context, fingerprint string) error {
	s.mu.Lock()
	run := s.test
	var d Draft
	switch {
	case run == nil || run.result.Status != TestPassed || s.now().Sub(run.passedAt) > testPassTTL:
		s.mu.Unlock()
		return ErrNoPassedTest
	case fingerprint == "" || fingerprint != run.result.Fingerprint || run.draft.fingerprint != run.result.Fingerprint:
		s.mu.Unlock()
		return ErrStaleTest
	}
	d = run.draft
	s.mu.Unlock()
	ctx = audit.With(ctx, "operator", audit.UI)
	err := s.d.WithTx(ctx, func(tx *sql.Tx) error {
		keys := make([]string, 0, len(d.changed))
		for k := range d.changed {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			var err error
			if d.changed[key] == "" && key != KeyAccessGroup && key != KeyAdminGroup {
				err = settings.Default.Unset(ctx, tx, key, 0)
			} else {
				err = settings.Default.Set(ctx, tx, key, d.changed[key], 0)
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
