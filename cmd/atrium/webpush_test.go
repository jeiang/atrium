package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/netguard"
	"github.com/jeiang/atrium/internal/notify"
)

type pushRequest struct {
	path   string
	header http.Header
	body   []byte
}

type fakePush struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	route    func(*http.Request) int
	redirect string
	received []pushRequest
	arrived  chan pushRequest
}

func newFakePush(t *testing.T) *fakePush {
	f := &fakePush{status: http.StatusCreated, arrived: make(chan pushRequest, 16)}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		req := pushRequest{path: r.URL.Path, header: r.Header.Clone(), body: body}
		f.received = append(f.received, req)
		status := f.status
		if f.route != nil {
			status = f.route(r)
		}
		f.mu.Unlock()
		select {
		case f.arrived <- req:
		default:
		}
		if status == http.StatusTemporaryRedirect {
			w.Header().Set("Location", f.redirect)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakePush) answer(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
}

func (f *fakePush) answerRoute(route func(*http.Request) int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.route = route
}

func (f *fakePush) requestFor(t *testing.T, path string) pushRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.received {
		if req.path == path {
			return req
		}
	}
	t.Fatalf("the push endpoint received nothing for %s", path)
	return pushRequest{}
}

func (f *fakePush) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakePush) last(t *testing.T) pushRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.received) == 0 {
		t.Fatal("the push endpoint received nothing")
	}
	return f.received[len(f.received)-1]
}

type pushBrowser struct {
	key      *ecdh.PrivateKey
	secret   []byte
	endpoint string
}

func (b pushBrowser) form() url.Values {
	return url.Values{
		"endpoint": {b.endpoint},
		"p256dh":   {base64.RawURLEncoding.EncodeToString(b.key.PublicKey().Bytes())},
		"auth":     {base64.RawURLEncoding.EncodeToString(b.secret)},
	}
}

func (b pushBrowser) decrypt(t *testing.T, body []byte) map[string]any {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("push body of %d bytes", len(body))
	}
	salt, idLen := body[:16], int(body[20])
	if rs := binary.BigEndian.Uint32(body[16:20]); rs < 18 || len(body) < 21+idLen {
		t.Fatalf("record size %d, key id length %d", rs, idLen)
	}
	serverKey, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatalf("server key: %v", err)
	}
	shared, err := b.key.ECDH(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), b.key.PublicKey().Bytes()...), serverKey.Bytes()...)
	ikm, err := hkdf.Key(sha256.New, shared, b.secret, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	end := len(plain) - 1
	for end >= 0 && plain[end] == 0 {
		end--
	}
	if end < 0 || plain[end] != 2 {
		t.Fatalf("no last-record delimiter in %q", plain)
	}
	var payload map[string]any
	if err := json.Unmarshal(plain[:end], &payload); err != nil {
		t.Fatalf("payload %q: %v", plain[:end], err)
	}
	return payload
}

func checkVAPID(t *testing.T, header http.Header, publicKey, audience string) {
	t.Helper()
	match := regexp.MustCompile(`^vapid t=([^,]+), k=(.+)$`).FindStringSubmatch(header.Get("Authorization"))
	if match == nil || match[2] != publicKey {
		t.Fatalf("Authorization %q, want vapid with key %s", header.Get("Authorization"), publicKey)
	}
	parts := strings.Split(match[1], ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", match[1])
	}
	raw, err := base64.RawURLEncoding.DecodeString(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("signature: %v", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the VAPID token signature does not verify against the page key")
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var c struct{ Aud, Sub string }
	if err := json.Unmarshal(claims, &c); err != nil || c.Aud != audience || c.Sub != "mailto:ops@example.test" {
		t.Fatalf("claims %s, want aud %s and the configured subject", claims, audience)
	}
}

func TestWebPushSubscribesDeliversEncryptedPayloadsAndDropsGoneSubscriptions(t *testing.T) {
	requireCipher(t)
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRigGenerated(t, dir)

	base, logs := startServeLogged(t, dir)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	get := func() (string, http.Header) {
		t.Helper()
		code, body, header := fetch(t, client, http.MethodGet, base+"/settings/notifications", nil)
		if code != http.StatusOK {
			t.Fatalf("notifications page: %d\n%s", code, logs.String())
		}
		return body, header
	}
	post := func(path string, form url.Values) (int, string) {
		t.Helper()
		get()
		if form == nil {
			form = url.Values{}
		}
		code, body, _ := fetch(t, client, http.MethodPost, base+"/settings/notifications"+path, form)
		return code, body
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	devices := func() int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM notification_devices").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	page, header := get()
	if !strings.Contains(header.Get("Content-Security-Policy"), "worker-src 'self'") {
		t.Errorf("CSP %q lacks worker-src 'self'", header.Get("Content-Security-Policy"))
	}
	found := regexp.MustCompile(`data-push-key="([A-Za-z0-9_-]{87})"`).FindStringSubmatch(page)
	if found == nil || !strings.Contains(page, "Enable push on this device") {
		t.Fatal("the page lacks the VAPID key or the enable control")
	}
	publicKey := found[1]
	if again, _ := get(); !strings.Contains(again, publicKey) {
		t.Error("the VAPID key changed between page loads")
	}
	var stored int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM connection_credentials WHERE connection_id = 'service/webpush' AND slot = 'vapid_private' AND source = 'database'").Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("stored VAPID credentials = %d, %v", stored, err)
	}
	code, _, swHeader := fetch(t, client, http.MethodGet, base+"/static/sw.js", nil)
	if code != http.StatusOK || swHeader.Get("Service-Worker-Allowed") != "/" {
		t.Errorf("service worker: %d %q", code, swHeader.Get("Service-Worker-Allowed"))
	}

	bad := browser.form()
	bad.Set("endpoint", "http://"+strings.TrimPrefix(push.URL, "https://")+"/push/device-1")
	if code, body := post("/push/subscriptions", bad); code != http.StatusUnprocessableEntity || !strings.Contains(body, "https") {
		t.Fatalf("plain http endpoint: %d", code)
	}
	if code, body := post("/push/subscriptions", browser.form()); code != http.StatusOK || !strings.Contains(body, `data-push-device="`+browser.endpoint+`"`) {
		t.Fatalf("subscribe: %d", code)
	}
	if code, _ := post("/push/subscriptions", browser.form()); code != http.StatusOK || devices() != 1 {
		t.Fatalf("resubscribe: %d, %d devices", code, devices())
	}

	payloadOf := func(n int) (pushRequest, map[string]any) {
		t.Helper()
		sent := push.wait(t, n)
		return sent, browser.decrypt(t, sent.body)
	}
	if ran := runApp(t, "live", "pause"); ran.code != 0 {
		t.Fatalf("live pause: %d %s", ran.code, ran.stderr)
	}
	sent, payload := payloadOf(1)
	if sent.header.Get("Content-Encoding") != "aes128gcm" || sent.header.Get("TTL") == "" || sent.header.Get("Urgency") != "high" {
		t.Errorf("push headers %v", sent.header)
	}
	checkVAPID(t, sent.header, publicKey, push.URL)
	if payload["title"] != "atrium: mail writes paused" || payload["body"] != "Changed by the operator." || payload["url"] != "/settings/notifications" || len(payload) != 3 {
		t.Errorf("payload %v", payload)
	}
	var delivered bool
	if err := d.Read.QueryRow("SELECT delivered FROM notifications ORDER BY id DESC LIMIT 1").Scan(&delivered); err != nil || !delivered {
		t.Errorf("inbox row delivered = %v, %v", delivered, err)
	}
	if code, _ := post("/channels/system/content", url.Values{"content": {"title"}}); code != http.StatusOK {
		t.Fatalf("content: %d", code)
	}
	if ran := runApp(t, "live", "resume"); ran.code != 0 {
		t.Fatalf("live resume: %d %s", ran.code, ran.stderr)
	}
	if _, payload := payloadOf(2); payload["title"] != "atrium" || payload["body"] != "Open atrium to read this notification." {
		t.Errorf("title-only payload %v", payload)
	}

	if code, body := post("/push/test", nil); code != http.StatusOK || !strings.Contains(body, "Test sent") {
		t.Fatalf("push test: %d", code)
	}
	if payload := browser.decrypt(t, push.last(t).body); payload["title"] != "atrium test" || payload["body"] != "Test push notification." {
		t.Errorf("test payload %v", payload)
	}
	var lastOK *float64
	if err := d.Read.QueryRow("SELECT last_ok_ts FROM notification_devices").Scan(&lastOK); err != nil || lastOK == nil {
		t.Errorf("last_ok_ts = %v, %v", lastOK, err)
	}

	push.answer(http.StatusInternalServerError)
	for range notify.FailingAfter {
		if code, body := post("/push/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "web push 500") {
			t.Fatalf("failing endpoint: %d", code)
		}
	}
	var failures int
	if err := d.Read.QueryRow("SELECT failure_count FROM notification_devices").Scan(&failures); err != nil || failures != notify.FailingAfter {
		t.Errorf("failure_count = %d, %v", failures, err)
	}

	guarded := notify.UsePushAddresses(netguard.Public)
	before := push.count()
	if code, body := post("/push/test", nil); code != http.StatusUnprocessableEntity || !strings.Contains(body, "non-public address") || push.count() != before {
		t.Fatalf("loopback endpoint under the real guard: %d, %d requests", code, push.count()-before)
	}
	guarded()

	push.answer(http.StatusGone)
	if ran := runApp(t, "live", "pause"); ran.code != 0 {
		t.Fatalf("live pause: %d %s", ran.code, ran.stderr)
	}
	var failed int64
	var failure string
	if err := d.Read.QueryRow("SELECT id, COALESCE(error, '') FROM notifications ORDER BY id DESC LIMIT 1").Scan(&failed, &failure); err != nil || !strings.Contains(failure, "410") {
		t.Errorf("inbox error %q, %v", failure, err)
	}
	if n := devices(); n != 0 {
		t.Fatalf("%d subscriptions after a 410, want 0", n)
	}
	push.answer(http.StatusCreated)
	post("/push/subscriptions", browser.form())
	get()
	if code, _, _ := fetch(t, client, http.MethodPost, base+"/inbox/"+strconv.FormatInt(failed, 10)+"/resend", url.Values{}); code != http.StatusOK {
		t.Fatalf("resend: %d", code)
	}
	if err := d.Read.QueryRow("SELECT delivered FROM notifications WHERE id = ?", failed).Scan(&delivered); err != nil || !delivered {
		t.Errorf("resent row delivered = %v, %v", delivered, err)
	}

	push.answer(http.StatusCreated)
	post("/push/subscriptions", browser.form())
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM notification_devices").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if code, body := post("/push/"+strconv.FormatInt(id, 10)+"/remove", nil); code != http.StatusOK || strings.Contains(body, "data-push-device=") || devices() != 0 {
		t.Fatalf("remove: %d, %d devices", code, devices())
	}
	post("/push/subscriptions", browser.form())
	if code, _ := post("/push/unsubscribe", url.Values{"endpoint": {browser.endpoint}}); code != http.StatusOK || devices() != 0 {
		t.Fatalf("unsubscribe: %d, %d devices", code, devices())
	}
}

func TestConnectionsCLIWebPushTestDeliversDropsGoneAndBlocksPrivateEndpoints(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if ran := runApp(t, "connections", "list"); ran.code != 0 {
		t.Fatalf("connections list: exit %d stderr %q", ran.code, ran.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	browser.store(t, d)
	gone := browser
	gone.endpoint = push.URL + "/push/gone"
	gone.store(t, d)
	push.answerRoute(func(r *http.Request) int {
		if r.URL.Path == "/push/gone" {
			return http.StatusGone
		}
		return http.StatusCreated
	})

	tested := runApp(t, "connections", "test", "service/webpush", "--json")
	if tested.code != 0 || !strings.Contains(tested.stdout, "delivered to 1 device(s)") {
		t.Fatalf("connections test: exit %d stdout %q stderr %q", tested.code, tested.stdout, tested.stderr)
	}
	payload := browser.decrypt(t, push.requestFor(t, "/push/device-1").body)
	if payload["title"] != "atrium test" || payload["body"] != "Test push notification." {
		t.Errorf("diagnostic payload %v", payload)
	}
	var goneLeft int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM webpush_devices WHERE endpoint = ?", gone.endpoint).Scan(&goneLeft); err != nil || goneLeft != 0 {
		t.Fatalf("the 410 device is still stored: %d, %v", goneLeft, err)
	}
	var left int
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notification_devices").Scan(&left); err != nil || left != 1 {
		t.Fatalf("%d subscriptions after the 410, want the one that answered", left)
	}

	evil := browser
	evil.endpoint = "https://10.0.0.1/push/evil"
	evil.store(t, d)
	before := push.count()
	blocked := runApp(t, "connections", "test", "service/webpush", "--json")
	if blocked.code != 0 || !strings.Contains(blocked.stdout, "delivered to 1 device(s)") {
		t.Fatalf("connections test with a private endpoint: exit %d stdout %q stderr %q", blocked.code, blocked.stdout, blocked.stderr)
	}
	if got := push.count() - before; got != 1 {
		t.Fatalf("the fake endpoint received %d requests, want only the public device", got)
	}
	var failure string
	if err := d.Read.QueryRow("SELECT COALESCE(d.last_error, '') FROM notification_devices d JOIN webpush_devices w ON w.device_id = d.id WHERE w.endpoint = ?", evil.endpoint).Scan(&failure); err != nil || !strings.Contains(failure, "non-public address") {
		t.Fatalf("private endpoint error %q, %v", failure, err)
	}
	if err := d.Read.QueryRow("SELECT COUNT(*) FROM notification_devices").Scan(&left); err != nil || left != 2 {
		t.Fatalf("%d subscriptions after the blocked device, want it kept", left)
	}
}

var allowLoopbackPush sync.Once

func writeVAPIDKey(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vapid.key")
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(key.Bytes())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func webPushConnectionTable(ca, vapid string) string {
	var b strings.Builder
	b.WriteString("\n[connections.\"service/webpush\"]\nkind = \"service/webpush\"\nlabel = \"Web push\"\n")
	fmt.Fprintf(&b, "config = { subject = %q }\n", "mailto:ops@example.test")
	fmt.Fprintf(&b, "transport = { ca_file = %q }\n", ca)
	b.WriteString("capabilities = [\"delivery\"]\n")
	if vapid != "" {
		fmt.Fprintf(&b, "credential_files = { vapid_private = %q }\n", vapid)
	}
	return b.String()
}

func writeHostFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func webPushHostConfig(t *testing.T, dir, ca, vapid string) string {
	t.Helper()
	return hostConfigEdit(t, dir, "webpush-host.toml", map[string]string{
		defaultWebPushConnection: webPushConnectionTable(ca, vapid),
	})
}

func webPushOnlyHostConfig(t *testing.T, dir, ca, vapid string) string {
	t.Helper()
	return writeHostFile(t, dir, "webpush-only-host.toml", webPushConnectionTable(ca, vapid))
}

func pushRig(t *testing.T, dir string) (*fakePush, pushBrowser) {
	return pushRigWith(t, dir, true)
}

func pushRigGenerated(t *testing.T, dir string) (*fakePush, pushBrowser) {
	return pushRigWith(t, dir, false)
}

func pushRigWith(t *testing.T, dir string, hostCredential bool) (*fakePush, pushBrowser) {
	t.Helper()
	push := newFakePush(t)
	ca := filepath.Join(dir, "push-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: push.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	vapid := ""
	if hostCredential {
		vapid = writeVAPIDKey(t, dir)
	}
	setEnv(t, "ATRIUM_HOST_CONFIG", webPushHostConfig(t, dir, ca, vapid))
	allowLoopbackPush.Do(func() { notify.UsePushAddresses(func(a netip.Addr) bool { return a.IsLoopback() }) })
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	browser := pushBrowser{key: key, secret: make([]byte, 16), endpoint: push.URL + "/push/device-1"}
	_, _ = rand.Read(browser.secret)
	return push, browser
}

func (b pushBrowser) store(t *testing.T, d *db.DB) {
	t.Helper()
	form := b.form()
	res, err := d.Write.Exec("INSERT INTO notification_devices (transport, label, created_ts) VALUES ('webpush', 'Test device', 1)")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Write.Exec("INSERT INTO webpush_devices (device_id, endpoint, p256dh, auth) VALUES (?, ?, ?, ?)",
		id, form.Get("endpoint"), form.Get("p256dh"), form.Get("auth")); err != nil {
		t.Fatal(err)
	}
}

func (f *fakePush) wait(t *testing.T, n int) pushRequest {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		f.mu.Lock()
		if len(f.received) >= n {
			got := f.received[n-1]
			f.mu.Unlock()
			return got
		}
		f.mu.Unlock()
	}
	t.Fatalf("%d pushes, want %d", f.count(), n)
	return pushRequest{}
}

func TestInboxActionsSurviveRestartAndResend(t *testing.T) {
	dir, api, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	s := newPhoneSite(t, dir, api)
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	t.Cleanup(func() { _ = d.Close() })
	if code, _ := s.post("/settings/notifications/push/subscriptions", browser.form()); code != http.StatusOK {
		t.Fatalf("subscribe = %d", code)
	}
	s.policy(phoneKind)
	type pushed struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		Actions []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"actions"`
	}
	payload := func(n int) pushed {
		t.Helper()
		var p pushed
		raw, err := json.Marshal(browser.decrypt(t, push.wait(t, n).body))
		if err != nil || json.Unmarshal(raw, &p) != nil {
			t.Fatalf("payload %d: %v", n, err)
		}
		return p
	}
	links := func(p pushed) []string {
		t.Helper()
		if len(p.Actions) != 2 || p.Actions[0].Title != "Approve" || p.Actions[1].Title != "Dismiss" {
			t.Fatalf("actions = %#v", p.Actions)
		}
		out := []string{}
		for _, a := range p.Actions {
			if !strings.HasPrefix(a.URL, "/actions/") {
				t.Fatalf("action url %q", a.URL)
			}
			out = append(out, a.URL)
		}
		return out
	}
	rows := func() int {
		t.Helper()
		var n int
		if err := d.Read.QueryRow("SELECT COUNT(*) FROM notifications").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	push.answer(http.StatusInternalServerError)
	s.propose(1)
	first := links(payload(1))
	if rows() != 1 {
		t.Fatalf("%d inbox rows after a failed push, want 1", rows())
	}
	var id, proposal int64
	var stored, failure string
	var delivered bool
	if err := d.Read.QueryRow("SELECT n.id, n.actions_json, n.delivered, COALESCE(n.error, ''), p.id FROM notifications n, proposals p").Scan(&id, &stored, &delivered, &failure, &proposal); err != nil {
		t.Fatal(err)
	}
	if delivered || !strings.Contains(failure, "web push 500") {
		t.Fatalf("failed push recorded delivered=%v error=%q", delivered, failure)
	}
	var refs []notify.ActionRef
	if err := json.Unmarshal([]byte(stored), &refs); err != nil || len(refs) != 2 {
		t.Fatalf("actions_json %q: %v", stored, err)
	}
	for i, decision := range []string{"approve", "dismiss"} {
		if refs[i].Kind != "proposal" || refs[i].TargetID != proposal || refs[i].Decision != decision {
			t.Fatalf("action reference %d = %#v", i, refs[i])
		}
	}
	for _, link := range first {
		if strings.Contains(stored, strings.TrimPrefix(link, "/actions/")) {
			t.Fatal("the inbox row stores a phone link token")
		}
	}
	var failures int
	if err := d.Read.QueryRow("SELECT failure_count FROM notification_devices").Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("failure_count = %d, %v", failures, err)
	}

	s.stop()
	s.restart()
	resend := "/inbox/" + strconv.FormatInt(id, 10) + "/resend"
	s.policy("reminder")
	if code, _ := s.post(resend, nil); code == http.StatusOK {
		t.Fatal("a failing resend reported delivery")
	}
	if p := payload(2); len(p.Actions) != 0 {
		t.Fatalf("a disallowed kind presented phone actions %#v", p.Actions)
	}
	s.policy(phoneKind)
	push.answer(http.StatusCreated)
	if code, _ := s.post(resend, nil); code != http.StatusOK {
		t.Fatalf("resend = %d", code)
	}
	again := links(payload(3))
	if again[0] == first[0] || again[1] == first[1] {
		t.Fatal("the resend reused the first phone links")
	}
	if err := d.Read.QueryRow("SELECT delivered FROM notifications WHERE id = ?", id).Scan(&delivered); err != nil || !delivered || rows() != 1 {
		t.Fatalf("after resend delivered=%v rows=%d, %v", delivered, rows(), err)
	}
	if code := s.action(strings.TrimPrefix(again[1], "/actions/")); code != http.StatusOK {
		t.Fatalf("dismiss link = %d", code)
	}
	if code := s.action(strings.TrimPrefix(again[0], "/actions/")); code != http.StatusGone {
		t.Fatalf("the sibling approve link still acts after dismiss: %d", code)
	}

	push.answer(http.StatusGone)
	if code, _ := s.post("/settings/notifications/push/test", nil); code == http.StatusOK {
		t.Fatal("a 410 test reported delivery")
	}
	var devices int
	if err := d.Read.QueryRow("SELECT (SELECT COUNT(*) FROM notification_devices) + (SELECT COUNT(*) FROM webpush_devices)").Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("%d device rows after a 410, %v", devices, err)
	}
	if rows() != 1 {
		t.Fatalf("%d inbox rows after the device was removed", rows())
	}
}

func TestWebPushRedirectDoesNotDeleteDevice(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	push, browser := pushRig(t, dir)
	if got := runApp(t, "connections", "list"); got.code != 0 {
		t.Fatal(got.stderr)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"), keyFor(t))
	defer func() { _ = d.Close() }()
	browser.store(t, d)
	push.redirect = push.URL + "/push/gone"
	push.answerRoute(func(r *http.Request) int {
		if r.URL.Path == "/push/gone" {
			return http.StatusGone
		}
		return http.StatusTemporaryRedirect
	})
	got := runApp(t, "connections", "test", "service/webpush", "--json")
	if got.code == 0 || push.count() != 1 {
		t.Fatalf("redirect test %d, %d requests: %s %s", got.code, push.count(), got.stdout, got.stderr)
	}
	var failures int
	if err := d.Read.QueryRow("SELECT failure_count FROM notification_devices").Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("redirect device failures %d: %v", failures, err)
	}
}
