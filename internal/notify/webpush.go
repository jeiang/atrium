package notify

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/netguard"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/secrets"
	"github.com/jeiang/atrium/internal/settings"
	"github.com/jeiang/atrium/internal/tool"
)

const (
	KeyPushSubject = "webpush_subject"
	KeyVAPIDKey    = "webpush_vapid_private_key"
	PushTransport  = "webpush"

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

func init() {
	settings.MustRegister(
		settings.Value(settings.UI, settings.Def{Key: KeyPushSubject, Module: module, Type: settings.String, Check: subjectCheck}),
		settings.Value(settings.Advanced, settings.Def{Key: transport.CAKey(PushTransport), Module: module, Type: settings.String}),
		settings.Secret(settings.SecretOpts{Encrypted: true, Generated: true}, settings.Def{
			Key: KeyVAPIDKey, Module: module, Type: settings.String, Check: vapidCheck}),
	)
}

type PushError string

func (e PushError) Error() string { return string(e) }

type pushBlocked string

func (e pushBlocked) Error() string { return string(e) }

type PushDevice struct {
	ID        int64    `json:"id"`
	Endpoint  string   `json:"endpoint"`
	Label     string   `json:"label"`
	CreatedTS float64  `json:"created_ts"`
	LastOKTS  *float64 `json:"last_ok_ts"`
	Failures  int      `json:"failure_count"`
	LastError *string  `json:"last_error"`
}

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
	return settings.Invalid("must be a mailto: or https: URL")
}

func vapidScalar(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if b, err := hex.DecodeString(value); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "=")); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, settings.Invalid("must be a P-256 private key as 64 hex digits or 43 base64url characters")
}

func vapidCheck(value string) error {
	scalar, err := vapidScalar(value)
	if err != nil {
		return err
	}
	if _, err := ecdh.P256().NewPrivateKey(scalar); err != nil {
		return settings.Invalid("is not a valid P-256 private key")
	}
	return nil
}

func loadVAPID(ctx context.Context, q db.Querier) (pushKeys, error) {
	value, err := secrets.NewIn(settings.From(ctx), nil, q, nil).Generate(ctx, KeyVAPIDKey)
	if err != nil {
		return pushKeys{}, err
	}
	scalar, err := vapidScalar(value)
	if err != nil {
		return pushKeys{}, fmt.Errorf("%s: %w", KeyVAPIDKey, err)
	}
	key, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return pushKeys{}, fmt.Errorf("%s: %w", KeyVAPIDKey, err)
	}
	return pushKeys{
		private: base64.RawURLEncoding.EncodeToString(scalar),
		public:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
	}, nil
}

func PushPublicKey(ctx context.Context, q db.Querier) (string, error) {
	keys, err := loadVAPID(ctx, q)
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

func Subscribe(ctx context.Context, q db.Querier, endpoint, p256dh, auth, label string) error {
	endpoint = strings.TrimSpace(endpoint)
	if err := checkEndpoint(endpoint); err != nil {
		return err
	}
	public, err := pushKey(p256dh)
	if err != nil {
		return PushError("p256dh must be base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(public); err != nil {
		return PushError("p256dh is not a P-256 public key")
	}
	secret, err := pushKey(auth)
	if err != nil || len(secret) != 16 {
		return PushError("auth must be 16 bytes of base64url")
	}
	_, err = q.ExecContext(ctx, `INSERT INTO push_subscriptions (endpoint, p256dh, auth, label, created_ts) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, label = excluded.label, failure_count = 0, last_error = NULL`,
		endpoint, base64.RawURLEncoding.EncodeToString(public), base64.RawURLEncoding.EncodeToString(secret), Plain(label, pushLabelChars), stamp())
	return err
}

func Unsubscribe(ctx context.Context, q db.Querier, endpoint string) error {
	_, err := q.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE endpoint = ?", strings.TrimSpace(endpoint))
	return err
}

func RemoveDevice(ctx context.Context, q db.Querier, id int64) error {
	_, err := q.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE id = ?", id)
	return err
}

func Devices(ctx context.Context, q db.Querier) ([]PushDevice, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, endpoint, label, created_ts, last_ok_ts, failure_count, last_error FROM push_subscriptions ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (PushDevice, error) {
		var d PushDevice
		return d, r.Scan(&d.ID, &d.Endpoint, &d.Label, &d.CreatedTS, &d.LastOKTS, &d.Failures, &d.LastError)
	})
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

func deliverPush(ctx context.Context, q db.Querier, m delivery, link string) (tried, delivered bool, failure string, err error) {
	if m.channel == "" {
		m.channel = Assistant
	}
	var n int
	if err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM push_subscriptions").Scan(&n); err != nil || n == 0 {
		return false, false, "", err
	}
	c, err := GetChannel(ctx, q, m.channel)
	if err != nil {
		return true, false, "", err
	}
	title, body := m.title, tool.Clip(m.body, pushBodyChars)
	if !c.FullText() {
		title, body = privateTitle, privateBody
	}
	payload, err := json.Marshal(pushPayload{Title: title, Body: body, URL: pushURL(link, m.click), Actions: pushActions(m.actions)})
	if err != nil {
		return true, false, "", err
	}
	sent, failure, err := pushAll(ctx, q, m.channel, payload, urgency(m.priority))
	return true, sent > 0, failure, err
}

func PushTest(ctx context.Context, q db.Querier) (int, string, error) {
	payload, err := json.Marshal(pushPayload{Title: "atrium test", Body: "Test push notification.", URL: pushInbox})
	if err != nil {
		return 0, "", err
	}
	return pushAll(ctx, q, System, payload, webpush.UrgencyNormal)
}

type pushTarget struct {
	id                    int64
	endpoint, key, secret string
}

func pushTargets(ctx context.Context, q db.Querier) ([]pushTarget, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, endpoint, p256dh, auth FROM push_subscriptions ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (pushTarget, error) {
		var t pushTarget
		return t, r.Scan(&t.id, &t.endpoint, &t.key, &t.secret)
	})
}

func pushAll(ctx context.Context, q db.Querier, channel string, payload []byte, level webpush.Urgency) (sent int, failure string, err error) {
	targets, err := pushTargets(ctx, q)
	if err != nil || len(targets) == 0 {
		return 0, "", err
	}
	subject, err := text(ctx, q, KeyPushSubject)
	if err != nil {
		return 0, "", err
	}
	if subject == "" {
		return 0, "web push not configured (" + KeyPushSubject + ")", nil
	}
	keys, err := loadVAPID(ctx, q)
	if err != nil {
		return 0, "", err
	}
	client, err := pushClient(ctx, q)
	if err != nil {
		var te transport.Error
		if errors.As(err, &te) {
			return 0, "web push unreachable: " + te.Error(), nil
		}
		return 0, "", err
	}
	defer client.CloseIdleConnections()
	options := &webpush.Options{
		HTTPClient: client, Subscriber: strings.TrimPrefix(subject, "mailto:"), TTL: pushTTL, Urgency: level,
		VAPIDPublicKey: keys.public, VAPIDPrivateKey: keys.private,
	}
	var failures []string
	for _, t := range targets {
		problem, err := pushOne(ctx, q, channel, t, payload, options)
		if err != nil {
			return sent, "", err
		}
		if problem == "" {
			sent++
		} else {
			failures = append(failures, problem)
		}
	}
	if sent > 0 {
		return sent, "", nil
	}
	return 0, strings.Join(failures, "; "), nil
}

func pushOne(ctx context.Context, q db.Querier, channel string, t pushTarget, payload []byte, options *webpush.Options) (failure string, err error) {
	ctx, span := obs.Start(ctx, "notify.deliver", trace.WithAttributes(
		obs.String(obs.NotifyChannel, channel),
		obs.String(obs.NotifyBackend, PushTransport),
	))
	attempted := false
	defer func() {
		ok := failure == "" && err == nil
		span.SetAttributes(obs.Bool(obs.NotifyDelivered, ok))
		if err != nil {
			obs.Fail(span, "error")
		} else if failure != "" {
			span.SetAttributes(obs.String(obs.ErrorType, "delivery_failed"))
		}
		span.End()
		if attempted {
			obs.NotifyDelivery.Add(1, channel, PushTransport, strconv.FormatBool(ok))
		}
	}()
	if problem := checkEndpoint(t.endpoint); problem != nil {
		return problem.Error(), recordPush(ctx, q, t.id, problem.Error())
	}
	attempted = true
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: t.endpoint, Keys: webpush.Keys{P256dh: t.key, Auth: t.secret},
	}, options)
	if err == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		failure = "web push unreachable: " + pushErrorClass(err)
		pushLog.Warn("web push delivery failed", logx.Err, failure)
		return failure, recordPush(ctx, q, t.id, failure)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		pushLog.Info("web push subscription expired; removed", logx.Reason, strconv.Itoa(resp.StatusCode))
		_, err := q.ExecContext(ctx, "DELETE FROM push_subscriptions WHERE id = ?", t.id)
		return fmt.Sprintf("web push %d: subscription removed", resp.StatusCode), err
	case resp.StatusCode >= 300:
		failure = fmt.Sprintf("web push %d", resp.StatusCode)
		pushLog.Warn("web push delivery failed", logx.Err, failure)
		return failure, recordPush(ctx, q, t.id, failure)
	}
	return "", recordPush(ctx, q, t.id, "")
}

func recordPush(ctx context.Context, q db.Querier, id int64, failure string) error {
	if failure == "" {
		_, err := q.ExecContext(ctx, "UPDATE push_subscriptions SET failure_count = 0, last_error = NULL, last_ok_ts = ? WHERE id = ?", stamp(), id)
		return err
	}
	_, err := q.ExecContext(ctx, "UPDATE push_subscriptions SET failure_count = failure_count + 1, last_error = ? WHERE id = ?", failure, id)
	return err
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

func pushClient(ctx context.Context, q db.Querier) (*http.Client, error) {
	client, err := transport.Configured(ctx, q, PushTransport, pushTimeout)
	if err != nil {
		return nil, err
	}
	t, ok := client.Transport.(*http.Transport)
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
	return client, nil
}

func failingDevices(ctx context.Context, q db.Querier) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT label FROM push_subscriptions WHERE failure_count >= ? ORDER BY id", FailingAfter)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (string, error) {
		var label string
		return label, r.Scan(&label)
	})
}
