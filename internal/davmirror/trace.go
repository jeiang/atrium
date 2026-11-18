package davmirror

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/connections"
)

const (
	traceBodyLimit  = 800
	traceRepeatGap  = 3 * time.Second
	traceRepeatMost = 5
)

type stepKey struct{}

var (
	traceAddress = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	tracePathID  = regexp.MustCompile(`/([0-9]+)(/|"|<|\s|$)`)
	traceLongID  = regexp.MustCompile(`[0-9]{5,}`)
)

type tracer struct {
	out      io.Writer
	next     http.RoundTripper
	secrets  []*regexp.Regexp
	password *regexp.Regexp
	key      []byte
	n        int
	refused  *http.Request
	step     string
}

func traceICloud(ctx context.Context, lease *connections.Lease, req connections.TraceRequest) (connections.Result, error) {
	t := &tracer{out: req.Out}
	session, early, err := openICloud(ctx, lease, func(next http.RoundTripper) http.RoundTripper {
		if inner, ok := next.(interface{ Unwrap() http.RoundTripper }); ok {
			next = inner.Unwrap()
		}
		t.next = next
		return t
	})
	if err != nil {
		return connections.Result{}, err
	}
	if early != nil {
		early.Capability = req.Capability
		return *early, nil
	}
	t.key = []byte(session.password)
	t.password = regexp.MustCompile(regexp.QuoteMeta(session.password))
	user, source := session.user(req.Capability)
	for _, secret := range []string{user, session.account.Login} {
		if secret = strings.TrimSpace(secret); secret != "" {
			t.secrets = append(t.secrets, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(secret)))
		}
	}
	_, endpoint, _ := session.kind(req.Capability)
	same := "no"
	if strings.EqualFold(user, session.account.Login) {
		same = "yes"
	}
	_, _ = fmt.Fprintf(t.out, "capability: %s\nendpoint: %s\nuser: %s, from %s; the same as the mail login: %s\n",
		req.Capability, t.maskURL(endpoint), t.mask(user), source, same)
	result, _ := session.check(ctx, req.Capability, "", 1)
	t.repeat(ctx, min(req.Repeat, traceRepeatMost))
	return result, nil
}

func (t *tracer) repeat(ctx context.Context, times int) {
	if times <= 0 {
		return
	}
	if t.refused == nil {
		_, _ = fmt.Fprintf(t.out, "\nrepeat: no request was refused, so there is nothing to repeat\n")
		return
	}
	last, step := t.refused, t.step
	refused := 0
	for i := 1; i <= times; i++ {
		timer := time.NewTimer(traceRepeatGap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		again := last.Clone(context.WithValue(ctx, stepKey{}, fmt.Sprintf("repeat %d of %d of the %s", i, times, step)))
		if last.GetBody != nil {
			again.Body, _ = last.GetBody()
		}
		t.refused = nil
		resp, err := t.RoundTrip(again)
		if err != nil || t.refused != nil {
			refused++
		}
		if err == nil {
			_ = resp.Body.Close()
		}
	}
	_, _ = fmt.Fprintf(t.out, "\nrepeat: the %s request failed %d of %d times\n", step, refused, times)
}

func (t *tracer) hash(value string) string {
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(strings.ToLower(value)))
	sum := mac.Sum(nil)
	out := make([]byte, 6)
	for i := range out {
		out[i] = 'a' + sum[i]%16
	}
	return string(out)
}

func (t *tracer) maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return t.mask(raw)
	}
	return u.Scheme + "://" + u.Host + t.mask(u.RequestURI())
}

func (t *tracer) mask(s string) string {
	if t.password != nil {
		s = t.password.ReplaceAllString(s, "<password>")
	}
	for _, secret := range t.secrets {
		s = secret.ReplaceAllStringFunc(s, func(m string) string { return "<user:" + t.hash(m) + ">" })
	}
	s = traceAddress.ReplaceAllStringFunc(s, func(m string) string { return "<address:" + t.hash(m) + ">" })
	for range 2 {
		s = tracePathID.ReplaceAllStringFunc(s, func(m string) string {
			parts := tracePathID.FindStringSubmatch(m)
			return "/<n:" + t.hash(parts[1]) + ">" + parts[2]
		})
	}
	return traceLongID.ReplaceAllStringFunc(s, func(m string) string { return "<n:" + t.hash(m) + ">" })
}

func (t *tracer) RoundTrip(req *http.Request) (*http.Response, error) {
	t.n++
	step, _ := req.Context().Value(stepKey{}).(string)
	var b strings.Builder
	fmt.Fprintf(&b, "\n#%d %s step\n> %s %s\n", t.n, step, req.Method, t.maskURL(req.URL.String()))
	fmt.Fprintf(&b, "> header names: %s\n", strings.Join(slices.Sorted(maps.Keys(req.Header)), ", "))
	authorization := "no"
	if req.Header.Get("Authorization") != "" {
		authorization = "yes"
	}
	fmt.Fprintf(&b, "> authorization attached: %s\n> depth: %s\n> content-type: %s\n", authorization, req.Header.Get("Depth"), req.Header.Get("Content-Type"))
	if req.Header.Get("User-Agent") == "" {
		b.WriteString("> user-agent: not set, Go sends its default\n")
	}
	if req.GetBody != nil {
		if body, err := req.GetBody(); err == nil {
			raw, _ := io.ReadAll(body)
			fmt.Fprintf(&b, "> body: %s\n", t.mask(string(raw)))
		}
	}
	started := time.Now()
	resp, err := t.next.RoundTrip(req)
	elapsed := time.Since(started).Milliseconds()
	if err != nil {
		t.refused, t.step = req, step
		fmt.Fprintf(&b, "< error after %d ms: %s\n", elapsed, t.mask(err.Error()))
		_, _ = io.WriteString(t.out, b.String())
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if resp.StatusCode >= http.StatusBadRequest {
		t.refused, t.step = req, step
	}
	fmt.Fprintf(&b, "< %s %s\n< time: %d ms\n", resp.Proto, resp.Status, elapsed)
	for _, name := range slices.Sorted(maps.Keys(resp.Header)) {
		for _, value := range resp.Header[name] {
			if name == "Set-Cookie" {
				value = "<value withheld>"
			}
			fmt.Fprintf(&b, "< %s: %s\n", name, t.mask(value))
		}
	}
	body := t.mask(string(raw))
	if len(body) > traceBodyLimit {
		body = strings.ToValidUTF8(body[:traceBodyLimit], "") + " …"
	}
	fmt.Fprintf(&b, "< body (%d bytes): %s\n", len(raw), body)
	if readErr != nil {
		fmt.Fprintf(&b, "< body read error: %s\n", t.mask(readErr.Error()))
		_, _ = io.WriteString(t.out, b.String())
		return nil, readErr
	}
	_, _ = io.WriteString(t.out, b.String())
	return resp, nil
}
