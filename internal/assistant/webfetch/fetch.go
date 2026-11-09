package webfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/idna"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/netguard"
)

var textTypes = []string{"text/html", "application/xhtml+xml", "text/plain"}

type Page struct {
	Body    []byte
	Charset string
	URL     string
}

type Fetcher struct {
	Allowed func(netip.Addr) bool
}

type Limits struct {
	MaxBytes     int
	Timeout      time.Duration
	MaxRedirects int
	PageChars    int
	TextDefault  int
	UserAgent    string
}

func ReadLimits(ctx context.Context, q db.Querier) (Limits, error) {
	r := assistant.Read(ctx, q)
	lim := Limits{
		MaxBytes:     r.Int(assistant.KeyWebfetchMaxBytes),
		Timeout:      time.Duration(r.Int(assistant.KeyWebfetchTimeout)) * time.Second,
		MaxRedirects: r.Int(assistant.KeyWebfetchMaxRedirects),
		PageChars:    r.Int(assistant.KeyWebfetchPageChars),
		TextDefault:  r.Int(assistant.KeyWebfetchTextDefault),
		UserAgent:    r.Text(assistant.KeyWebfetchUserAgent),
	}
	return lim, r.Err()
}

var fetchAllowed atomic.Pointer[func(netip.Addr) bool]

func UseAddresses(allow func(netip.Addr) bool) (restore func()) {
	previous := fetchAllowed.Swap(&allow)
	return func() { fetchAllowed.Store(previous) }
}

func (f *Fetcher) allowed(ip netip.Addr) bool {
	if f.Allowed != nil {
		return f.Allowed(ip)
	}
	if allow := fetchAllowed.Load(); allow != nil {
		return (*allow)(ip)
	}
	return netguard.Public(ip)
}

func (f *Fetcher) FetchPage(ctx context.Context, rawURL string, lim Limits) (Page, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Now().Add(lim.Timeout))
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Page{}, FetchError(err.Error())
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" || req.URL.Hostname() == "" {
		return Page{}, Blocked("unsupported URL " + strconv.Quote(rawURL))
	}
	ascii, err := asciiHost(req.URL.Hostname())
	if err != nil {
		return Page{}, err
	}
	if ascii != req.URL.Hostname() {
		host := ascii
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if port := req.URL.Port(); port != "" {
			host += ":" + port
		}
		req.URL.Host = host
	}
	req.Header.Set("User-Agent", lim.UserAgent)
	req.Header.Set("Accept", strings.Join(textTypes, ", "))
	req.Header.Set("Accept-Encoding", "identity")

	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		ForceAttemptHTTP2:  false,
		DialContext:        f.dialContext,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > lim.MaxRedirects {
				return FetchError("too many redirects")
			}
			if next.URL.Scheme != "http" && next.URL.Scheme != "https" {
				return Blocked("unsupported URL " + strconv.Quote(next.URL.String()))
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return Page{}, fetchError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Page{}, FetchError(fmt.Sprintf("status %d", resp.StatusCode))
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/html"
	}
	kind, _, _ := strings.Cut(contentType, ";")
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !slices.Contains(textTypes, kind) {
		return Page{}, FetchError("content type " + kind)
	}
	if resp.ContentLength > int64(lim.MaxBytes) {
		return Page{}, FetchError("too large")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(lim.MaxBytes)+1))
	if err != nil {
		return Page{}, fetchError(ctx, err)
	}
	if len(body) > lim.MaxBytes {
		return Page{}, FetchError("too large")
	}
	return Page{Body: body, Charset: contentCharset(resp.Header), URL: resp.Request.URL.String()}, nil
}

func (f *Fetcher) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, FetchError("dns: " + err.Error())
	}
	if len(addrs) == 0 {
		return nil, FetchError("dns: no address")
	}
	for i, a := range addrs {
		addrs[i] = a.Unmap().WithZone("")
	}
	for _, a := range addrs {
		if !f.allowed(a) {
			return nil, Blocked(host + " resolves to a non-public address")
		}
	}
	dialer := net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !f.allowed(ap.Addr()) {
			return Blocked(host + " resolves to a non-public address")
		}
		return nil
	}}
	return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].String(), port))
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func asciiHost(host string) (string, error) {
	if isASCII(host) {
		return host, nil
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", FetchError("invalid hostname " + strconv.Quote(host))
	}
	return ascii, nil
}

func fetchError(ctx context.Context, err error) error {
	var (
		fetch   FetchError
		blocked Blocked
	)
	switch {
	case errors.As(err, &fetch), errors.As(err, &blocked):
		return err
	case errors.Is(ctx.Err(), context.Canceled):
		return ctx.Err()
	case errors.Is(err, context.DeadlineExceeded):
		return FetchError("timeout")
	}
	return FetchError(err.Error())
}
