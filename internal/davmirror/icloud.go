package davmirror

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

const KindICloud connections.Kind = "icloud"

const (
	CapabilityCalDAV  = "caldav"
	CapabilityCardDAV = "carddav"
)

const (
	AppleIDKey = "icloud_apple_id"
	AccountKey = "calendar_account"
)

const (
	CalDAVURL  = "https://caldav.icloud.com/"
	CardDAVURL = "https://contacts.icloud.com/"
	CalDAVEnv  = "ATRIUM_CALENDAR_CALDAV_URL"
	CardDAVEnv = "ATRIUM_CALENDAR_CARDDAV_URL"
)

const remoteTimeout = 60 * time.Second

type Endpoints struct {
	CalDAV  string
	CardDAV string
}

func EndpointsFrom(env config.Env) Endpoints {
	pick := func(key, fallback string) string {
		if env != nil {
			if value, ok := env(key); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
		return fallback
	}
	return Endpoints{CalDAV: pick(CalDAVEnv, CalDAVURL), CardDAV: pick(CardDAVEnv, CardDAVURL)}
}

type Login struct {
	AppleID string
	Account string
	DAV     *connections.Lease
}

func LoadLogin(ctx context.Context, q db.Querier) (Login, error) {
	var out Login
	for _, step := range []struct {
		key  string
		into *string
	}{{AppleIDKey, &out.AppleID}, {AccountKey, &out.Account}} {
		found, err := settings.From(ctx).Resolve(ctx, q, step.key)
		if err != nil {
			return Login{}, err
		}
		*step.into = strings.TrimSpace(found.Value)
	}
	if account, err := icloudAccount(ctx, out); err == nil {
		out.DAV = openConnection(ctx, q, ICloudID(account.Name))
	}
	return out, nil
}

func openConnection(ctx context.Context, q db.Querier, id connections.ID) *connections.Lease {
	var (
		lease *connections.Lease
		err   error
	)
	if store := connections.DefaultFor(q); store != nil {
		lease, err = store.Open(ctx, id)
	} else {
		lease, err = connections.OpenRead(ctx, q, config.From(ctx).Host(), id)
	}
	if err != nil {
		return nil
	}
	return lease
}

func ICloudID(account string) connections.ID { return connections.ID("icloud/" + account) }

func icloudAccount(ctx context.Context, login Login) (config.AccountSpec, error) {
	var found []config.AccountSpec
	for _, a := range config.From(ctx).Accounts() {
		if a.Enabled && a.Preset == config.ICloud && (login.Account == "" || a.Name == login.Account) {
			found = append(found, a)
		}
	}
	switch {
	case len(found) == 0 && login.Account != "":
		return config.AccountSpec{}, config.NotConfigured(AccountKey + " " + strconv.Quote(login.Account) + " is not an enabled iCloud mail account")
	case len(found) == 0:
		return config.AccountSpec{}, config.NotConfigured("no enabled iCloud mail account")
	case len(found) > 1:
		return config.AccountSpec{}, config.NotConfigured("several iCloud mail accounts; set " + AccountKey)
	}
	return found[0], nil
}

func ICloudLogin(ctx context.Context, login Login) (config.CalDAVLogin, error) {
	account, err := icloudAccount(ctx, login)
	if err != nil {
		return config.CalDAVLogin{}, err
	}
	apple := login.AppleID
	if apple == "" {
		apple = account.Login
	}
	password := ""
	if login.DAV != nil && login.DAV.ID() == ICloudID(account.Name) {
		password, _ = icloudPassword(ctx, login.DAV, account.Name)
	}
	if password == "" {
		password, _ = config.From(ctx).Password(account.Name)
	}
	password = config.NormalizePassword(password)
	if password == "" {
		return config.CalDAVLogin{}, config.NotConfigured("the iCloud app password is not set")
	}
	return config.CalDAVLogin{AppleID: apple, Password: password}, nil
}

func DAVClient(ctx context.Context, login Login, endpoint, capability string) (*Client, error) {
	creds, err := ICloudLogin(ctx, login)
	if err != nil {
		return nil, err
	}
	base := &http.Client{Timeout: remoteTimeout}
	if login.DAV != nil {
		if client, err := login.DAV.Transport(); err == nil {
			base = client
		}
	}
	client, err := New(BasicClient(base, creds.AppleID, creds.Password), endpoint)
	if err != nil {
		return nil, err
	}
	client.cache = cacheKey(capability, endpoint, creds.AppleID, creds.Password)
	return client, nil
}

func Configured(ctx context.Context, login Login, dir string) error {
	if dir == "" {
		return config.NotConfigured("the mirror directory is not set")
	}
	_, err := ICloudLogin(ctx, login)
	return err
}

func SyncCalendars(ctx context.Context, env config.Env, login Login, dir string) (Stats, error) {
	return mirror(ctx, login, EndpointsFrom(env).CalDAV, dir, Calendars, CapabilityCalDAV)
}

func SyncContacts(ctx context.Context, env config.Env, login Login, dir string) (Stats, error) {
	return mirror(ctx, login, EndpointsFrom(env).CardDAV, dir, Contacts, CapabilityCardDAV)
}

func mirror(ctx context.Context, login Login, endpoint, dir string, k Kind, capability string) (Stats, error) {
	if dir == "" {
		return Stats{}, errors.New("the mirror directory is not set")
	}
	client, err := DAVClient(ctx, login, endpoint, capability)
	if err != nil {
		return Stats{}, err
	}
	syncOutcomes.Delete(client.cache)
	collections, err := client.Fetch(ctx, k)
	if result, ok := outcome(capability, err); ok && ctx.Err() == nil {
		rememberSync(client.cache, result)
		if login.DAV != nil {
			login.DAV.Report(result)
		}
	}
	if err != nil {
		return Stats{}, err
	}
	return Apply(dir, k.Ext, collections)
}

func outcome(capability string, err error) (connections.Result, bool) {
	if err == nil {
		return connections.Result{Capability: capability, State: connections.OK, Reason: "the mirror sync works"}, true
	}
	return failure(capability, err)
}

func failure(capability string, err error) (connections.Result, bool) {
	if status, ok := errors.AsType[*StatusError](err); ok {
		switch {
		case status.Refused() && status.Step == StepRoot:
			return connections.Result{Capability: capability, State: connections.NeedsReauth, Reason: "iCloud refused the app password"}, true
		case status.Refused():
			reason := "iCloud accepted the app password but refused " + status.Refusal()
			for i, other := range status.Also {
				if i == 0 {
					reason += "; also refused "
				} else {
					reason += " and "
				}
				reason += other.Refusal()
			}
			return connections.Result{Capability: capability, State: connections.Failing, Reason: reason}, true
		case status.Status >= http.StatusInternalServerError:
			return connections.Result{Capability: capability, State: connections.Failing, Reason: fmt.Sprintf("iCloud failed the %s request on %s (HTTP %d)", status.Step, status.Host, status.Status)}, true
		}
		return connections.Result{}, false
	}
	if untrusted, ok := errors.AsType[*UntrustedError](err); ok {
		return connections.Result{Capability: capability, State: connections.Failing, Reason: untrusted.Error()}, true
	}
	if errors.As(err, new(net.Error)) || errors.As(err, new(*tls.CertificateVerificationError)) ||
		errors.As(err, new(tls.AlertError)) || errors.As(err, new(tls.RecordHeaderError)) {
		return connections.Result{Capability: capability, State: connections.Failing, Reason: err.Error()}, true
	}
	return connections.Result{}, false
}

func Connections() []connections.Definition {
	return []connections.Definition{{Kind: KindICloud, Offers: []string{CapabilityCalDAV, CapabilityCardDAV}, CredentialSlots: []string{"mail/*/password"}, Shares: sharedPassword, Validate: validateICloud, Test: testICloud, Silent: silentICloud, Trace: traceICloud}}
}

func sharedPassword(id connections.ID) (connections.ID, string) {
	account := strings.TrimPrefix(string(id), "icloud/")
	return connections.ID("mail/" + account), "mail/" + account + "/password"
}

func validateICloud(c connections.Candidate) error {
	var cfg map[string]string
	if len(c.Config) > 0 {
		if err := json.Unmarshal(c.Config, &cfg); err != nil {
			return fmt.Errorf("icloud config: %w", err)
		}
	}
	for key, value := range cfg {
		switch key {
		case "apple_id":
		case "caldav_url", "carddav_url":
			if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
				return fmt.Errorf("icloud %s must be an http or https URL", key)
			}
		default:
			return fmt.Errorf("icloud config has no %s field", key)
		}
	}
	for _, cap := range c.Capabilities {
		if cap.Name != CapabilityCalDAV && cap.Name != CapabilityCardDAV {
			return fmt.Errorf("icloud has no %s capability", cap.Name)
		}
	}
	return nil
}

func icloudPassword(ctx context.Context, lease *connections.Lease, account string) (string, error) {
	password, err := rawICloudPassword(ctx, lease, account)
	if password = config.NormalizePassword(password); err == nil && password == "" {
		err = errors.New("the iCloud app password is not set")
	}
	return password, err
}

func rawICloudPassword(ctx context.Context, lease *connections.Lease, account string) (string, error) {
	password, err := lease.OwnerCredential(ctx)
	if err == nil && password != "" {
		return password, nil
	}
	password, err = lease.Credential("mail/" + account + "/password")
	if err == nil && password != "" {
		return password, nil
	}
	password, _ = config.From(ctx).Password(account)
	if password == "" {
		return "", errors.New("the iCloud app password is not set")
	}
	return password, nil
}

func testICloud(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	return checkICloud(ctx, lease, caps, false)
}

func silentICloud(ctx context.Context, lease *connections.Lease, caps []connections.Capability) ([]connections.Result, error) {
	return checkICloud(ctx, lease, caps, true)
}

func checkICloud(ctx context.Context, lease *connections.Lease, caps []connections.Capability, reuse bool) ([]connections.Result, error) {
	session, early, err := openICloud(ctx, lease, nil)
	if err != nil || early != nil {
		return resultList(early), err
	}
	results := make([]connections.Result, 0, len(caps))
	for _, cap := range caps {
		if !cap.Enabled {
			continue
		}
		key := session.key(cap.Name)
		if synced, fresh := recentSync(key); reuse && fresh {
			results = append(results, synced)
			continue
		}
		if !reuse {
			discoveries.Delete(key)
		}
		results = append(results, session.check(ctx, cap.Name, key))
	}
	if len(results) == 0 {
		return []connections.Result{{Capability: CapabilityCalDAV, State: connections.Unconfigured, Reason: "no iCloud capability is enabled"}}, nil
	}
	return results, nil
}

func resultList(r *connections.Result) []connections.Result {
	if r == nil {
		return nil
	}
	return []connections.Result{*r}
}

type icloudSession struct {
	http      *http.Client
	user      string
	source    string
	login     string
	password  string
	endpoints Endpoints
}

func openICloud(ctx context.Context, lease *connections.Lease, wrap func(http.RoundTripper) http.RoundTripper) (icloudSession, *connections.Result, error) {
	account := strings.TrimPrefix(string(lease.ID()), "icloud/")
	spec, ok := config.From(ctx).Account(account)
	if !ok || spec.Preset != config.ICloud {
		return icloudSession{}, &connections.Result{Capability: CapabilityCalDAV, State: connections.Unconfigured, Reason: "no enabled iCloud mail account"}, nil
	}
	password, err := icloudPassword(ctx, lease, account)
	if err != nil {
		return icloudSession{}, &connections.Result{Capability: CapabilityCalDAV, State: connections.NeedsReauth, Reason: err.Error()}, nil
	}
	var cfg map[string]string
	_ = json.Unmarshal(lease.Config(), &cfg)
	s := icloudSession{user: strings.TrimSpace(cfg["apple_id"]), source: "the connection's apple_id", login: spec.Login, password: password, endpoints: EndpointsFrom(config.OS)}
	if s.user == "" {
		s.user, s.source = spec.Login, "the mail account login"
	}
	base, err := lease.Transport()
	if err != nil {
		return icloudSession{}, nil, err
	}
	if wrap != nil {
		traced := *base
		traced.Transport = wrap(base.Transport)
		base = &traced
	}
	s.http = BasicClient(base, s.user, password)
	return s, nil, nil
}

func (s icloudSession) key(capability string) string {
	_, endpoint, _ := s.kind(capability)
	return cacheKey(capability, endpoint, s.user, s.password)
}

func (s icloudSession) kind(capability string) (Kind, string, bool) {
	switch capability {
	case CapabilityCalDAV:
		return Calendars, s.endpoints.CalDAV, true
	case CapabilityCardDAV:
		return Contacts, s.endpoints.CardDAV, true
	}
	return Kind{}, "", false
}

func (s icloudSession) check(ctx context.Context, capability, cache string) connections.Result {
	kind, endpoint, ok := s.kind(capability)
	if !ok {
		return connections.Result{Capability: capability, State: connections.Failing, Reason: "unknown iCloud capability"}
	}
	client, err := New(s.http, endpoint)
	if err != nil {
		return connections.Result{Capability: capability, State: connections.Failing, Reason: err.Error()}
	}
	client.cache, client.verify = cache, true
	collections, err := client.Collections(ctx, kind)
	if err != nil {
		result, ok := failure(capability, err)
		if !ok {
			result = connections.Result{Capability: capability, State: connections.Failing, Reason: err.Error()}
		}
		return result
	}
	data, _ := json.Marshal(map[string]any{"collections": len(collections)})
	reason := fmt.Sprintf("%d collections", len(collections))
	if len(collections) == 1 {
		reason = "1 collection"
	}
	return connections.Result{Capability: capability, State: connections.OK, Reason: reason, Data: data}
}
