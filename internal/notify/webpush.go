package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/netguard"
)

const (
	KindWebPush connections.Kind = "service/webpush"

	pushInbox       = "/settings/notifications"
	pushBodyChars   = 240
	pushLabelChars  = 80
	pushTTL         = 24 * 60 * 60
	pushTimeout     = 10 * time.Second
	pushMaxEndpoint = 2048
)

var (
	pushLog     = logx.Logger("atrium.notify.webpush")
	pushAllowed atomic.Pointer[func(netip.Addr) bool]
)

type PushError string

func (e PushError) Error() string { return string(e) }

type pushBlocked string

func (e pushBlocked) Error() string { return string(e) }

type pushAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type pushPayload struct {
	Title   string       `json:"title"`
	Body    string       `json:"body"`
	URL     string       `json:"url"`
	Actions []pushAction `json:"actions,omitempty"`
}

type pushKeys struct{ private, public string }

func UsePushAddresses(allow func(netip.Addr) bool) (restore func()) {
	previous := pushAllowed.Swap(&allow)
	return func() { pushAllowed.Store(previous) }
}

func subjectCheck(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	switch {
	case err == nil && u.Scheme == "mailto" && strings.Contains(u.Opaque, "@"):
		return nil
	case err == nil && u.Scheme == "https" && u.Host != "":
		return nil
	}
	return PushError("must be a mailto: or https: URL")
}

func vapidScalar(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if b, err := hex.DecodeString(value); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "=")); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, PushError("must be a P-256 private key as 64 hex digits or 43 base64url characters")
}

func vapidCheck(value string) error {
	scalar, err := vapidScalar(value)
	if err != nil {
		return err
	}
	if _, err := ecdh.P256().NewPrivateKey(scalar); err != nil {
		return PushError("is not a valid P-256 private key")
	}
	return nil
}

func generateVAPID() (string, error) {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.Bytes()), nil
}

func pushConnection(ctx context.Context, d *db.DB) (*connections.Lease, error) {
	reg, err := connections.From(ctx)
	if err != nil {
		return nil, err
	}
	store, err := connections.New(d, config.From(ctx).Host(), reg)
	if err != nil {
		return nil, err
	}
	id := connections.ID(KindWebPush)
	if err := store.Ensure(ctx, id, KindWebPush, "Web push", nil, []connections.Capability{{Name: "delivery", Enabled: true}}); err != nil {
		return nil, err
	}
	if _, err := store.EnsureCredential(ctx, id, "vapid_private", generateVAPID); err != nil {
		return nil, err
	}
	return connections.OpenRead(ctx, d.Read, config.From(ctx).Host(), id)
}

func connectionConfig(lease *connections.Lease) map[string]string {
	var m map[string]string
	_ = json.Unmarshal(lease.Config(), &m)
	return m
}

func vapidKeys(lease *connections.Lease) (pushKeys, error) {
	value, err := lease.Credential("vapid_private")
	if err != nil {
		return pushKeys{}, err
	}
	scalar, err := vapidScalar(value)
	if err != nil {
		return pushKeys{}, err
	}
	key, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return pushKeys{}, err
	}
	return pushKeys{
		private: base64.RawURLEncoding.EncodeToString(scalar),
		public:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
	}, nil
}

func EnsurePush(ctx context.Context, d *db.DB) error {
	lease, err := pushConnection(ctx, d)
	if err != nil {
		return err
	}
	lease.Close()
	return nil
}

func PushPublicKey(ctx context.Context, d *db.DB) (string, error) {
	lease, err := connections.OpenRead(ctx, d.Read, config.From(ctx).Host(), connections.ID(KindWebPush))
	if err != nil {
		return "", err
	}
	defer lease.Close()
	keys, err := vapidKeys(lease)
	return keys.public, err
}

func pushKey(value string) ([]byte, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "=")
	if b, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func checkEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > pushMaxEndpoint || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return PushError("push endpoint must be an https URL")
	}
	return nil
}

func PushLabel(userAgent string) string {
	browser := ""
	for _, b := range []struct{ token, name string }{
		{"Edg/", "Edge"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"}, {"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(userAgent, b.token) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, s := range []struct{ token, name string }{
		{"iPhone", "iOS"}, {"iPad", "iPadOS"}, {"Android", "Android"}, {"Mac OS X", "macOS"}, {"Windows", "Windows"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(userAgent, s.token) {
			system = s.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	return "Browser"
}

func urgency(priority int) webpush.Urgency {
	switch {
	case priority >= 4:
		return webpush.UrgencyHigh
	case priority == 2:
		return webpush.UrgencyLow
	case priority == 1:
		return webpush.UrgencyVeryLow
	}
	return webpush.UrgencyNormal
}

func pushURL(link, click string) string {
	for _, candidate := range []string{link, click} {
		if strings.HasPrefix(candidate, "/") && !strings.HasPrefix(candidate, "//") {
			return candidate
		}
	}
	return pushInbox
}

func pushActions(actions []Action) []pushAction {
	var out []pushAction
	for i, a := range actions {
		if strings.HasPrefix(a.URL, "/actions/") && strings.EqualFold(a.Method, http.MethodPost) {
			out = append(out, pushAction{Action: "post-" + strconv.Itoa(i), Title: a.Label, URL: a.URL})
		}
	}
	return out
}

func pushErrorClass(err error) string {
	var blocked pushBlocked
	if errors.As(err, &blocked) {
		return blocked.Error()
	}
	if errors.Is(err, webpush.ErrMaxPadExceeded) {
		return "payload too large"
	}
	return errorClass(err)
}

func pushClient(lease *connections.Lease) (*http.Client, error) {
	client, err := lease.Transport()
	if err != nil {
		return nil, err
	}
	t, ok := client.Transport.(*http.Transport)
	if !ok {
		if wrapped, is := client.Transport.(interface{ Unwrap() http.RoundTripper }); is {
			t, ok = wrapped.Unwrap().(*http.Transport)
		}
	}
	if !ok {
		return nil, fmt.Errorf("web push: unexpected transport %T", client.Transport)
	}
	allowed := netguard.Public
	if allow := pushAllowed.Load(); allow != nil {
		allowed = *allow
	}
	t.Proxy = nil
	t.DialContext = (&net.Dialer{Timeout: pushTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !allowed(ap.Addr().Unmap()) {
			return pushBlocked("endpoint resolves to a non-public address")
		}
		return nil
	}}).DialContext
	t.ForceAttemptHTTP2 = true
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client, nil
}
