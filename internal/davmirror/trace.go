package davmirror

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/connections"
)

const traceBodyLimit = 800

type stepKey struct{}

var (
	traceAddress = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	tracePathID  = regexp.MustCompile(`/[0-9]+(/|"|<|\s|$)`)
	traceLongID  = regexp.MustCompile(`[0-9]{5,}`)
)

type tracer struct {
	out     io.Writer
	next    http.RoundTripper
	secrets []*regexp.Regexp
	n       int
}

func traceICloud(ctx context.Context, lease *connections.Lease, capability string, out io.Writer) (connections.Result, error) {
	t := &tracer{out: out}
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
		early.Capability = capability
		return *early, nil
	}
	for _, secret := range []string{session.password, session.user, session.login} {
		if secret = strings.TrimSpace(secret); secret != "" {
			t.secrets = append(t.secrets, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(secret)))
		}
	}
	_, endpoint, _ := session.kind(capability)
	same := "no"
	if strings.EqualFold(session.user, session.login) {
		same = "yes"
	}
	_, _ = fmt.Fprintf(out, "capability: %s\nendpoint: %s\nuser: %s, from %s; the same as the mail login: %s\n",
		capability, t.maskURL(endpoint), t.mask(session.user), session.source, same)
	return session.check(ctx, capability), nil
}

func (t *tracer) maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return t.mask(raw)
	}
	return u.Scheme + "://" + u.Host + t.mask(u.RequestURI())
}

func (t *tracer) mask(s string) string {
	for _, secret := range t.secrets {
		s = secret.ReplaceAllString(s, "<user>")
	}
	s = traceAddress.ReplaceAllString(s, "<address>")
	for range 2 {
		s = tracePathID.ReplaceAllString(s, "/<n>$1")
	}
	return traceLongID.ReplaceAllString(s, "<n>")
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
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		fmt.Fprintf(&b, "< error: %s\n", t.mask(err.Error()))
		_, _ = io.WriteString(t.out, b.String())
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	fmt.Fprintf(&b, "< %s %s\n", resp.Proto, resp.Status)
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
