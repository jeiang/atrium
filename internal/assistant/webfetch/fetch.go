package webfetch

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const ReadBlock = 65536

var (
	textTypes        = []string{"text/html", "application/xhtml+xml", "text/plain"}
	redirectStatuses = []int{301, 302, 303, 307, 308}
)

type Page struct {
	Body    []byte
	Charset string
	URL     string
}

type Dest struct {
	Scheme  string
	Host    string
	Port    int
	Address netip.Addr
}

type Fetcher struct {
	Allowed func(netip.Addr) bool
	Lookup  func(ctx context.Context, host string) ([]netip.Addr, error)
	Connect func(ctx context.Context, dest Dest) (net.Conn, error)

	tlsRoots *x509.CertPool
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

func (f *Fetcher) allowed(ip netip.Addr) bool {
	if f.Allowed != nil {
		return f.Allowed(ip)
	}
	return DefaultAllowed(ip)
}

func (f *Fetcher) FetchPage(ctx context.Context, url string, lim Limits) (Page, error) {
	deadline := time.Now().Add(lim.Timeout)
	for range lim.MaxRedirects + 1 {
		parts, err := py.URLSplit(url)
		if err != nil {
			return Page{}, err
		}
		host, hasHost := parts.Hostname()
		if parts.Scheme != "http" && parts.Scheme != "https" || !hasHost {
			return Page{}, Blocked("unsupported URL " + py.Repr(url))
		}
		port, hasPort, err := parts.Port()
		if err != nil {
			return Page{}, err
		}
		if !hasPort || port == 0 {
			port = 80
			if parts.Scheme == "https" {
				port = 443
			}
		}
		ascii, err := asciiHost(host)
		if err != nil {
			return Page{}, err
		}
		bounded, cancelBound := context.WithDeadline(ctx, deadline)
		address, err := f.resolve(bounded, host, ascii)
		cancelBound()
		if err != nil {
			return Page{}, connectError(ctx, err)
		}
		next, page, err := f.hop(ctx, deadline, url, parts, Dest{Scheme: parts.Scheme, Host: ascii, Port: port, Address: address}, lim)
		if err != nil {
			return Page{}, err
		}
		if next == "" {
			return Page{Body: page.Body, Charset: page.Charset, URL: url}, nil
		}
		url = next
	}
	return Page{}, FetchError("too many redirects")
}

func (f *Fetcher) lookup(ctx context.Context, host string) ([]netip.Addr, error) {
	if f.Lookup != nil {
		return f.Lookup(ctx, host)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	for i, a := range addrs {
		addrs[i] = a.Unmap()
	}
	return addrs, err
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
		return "", &py.Exception{Class: "UnicodeError", Msg: "encoding with 'idna' codec failed (UnicodeError: " + err.Error() + ")"}
	}
	return ascii, nil
}

func (f *Fetcher) resolve(ctx context.Context, host, ascii string) (netip.Addr, error) {
	addrs, err := f.lookup(ctx, ascii)
	if err != nil {
		return netip.Addr{}, FetchError("dns: " + osText(err))
	}
	if len(addrs) == 0 {
		return netip.Addr{}, FetchError("dns: no address")
	}
	for _, a := range addrs {
		if !f.allowed(a.WithZone("")) {
			return netip.Addr{}, Blocked(host + " resolves to a non-public address")
		}
	}
	return addrs[0].WithZone(""), nil
}

func (f *Fetcher) dial(ctx context.Context, d Dest) (net.Conn, error) {
	dialer := net.Dialer{Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !f.allowed(ap.Addr()) {
			return Blocked(d.Host + " resolves to a non-public address")
		}
		return nil
	}}
	conn, err := dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(d.Address, uint16(d.Port)).String())
	if err != nil {
		return nil, err
	}
	if d.Scheme != "https" {
		return conn, nil
	}
	tc := tls.Client(conn, &tls.Config{ServerName: d.Host, RootCAs: f.tlsRoots})
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return tc, nil
}

func isControl(r rune) bool { return r <= 0x20 || r == 0x7f }

func hostHeader(d Dest) (string, error) {
	host := d.Host
	if i := strings.IndexFunc(host, isControl); i >= 0 {
		bad, _ := utf8.DecodeRuneInString(host[i:])
		return "", FetchError(fmt.Sprintf("URL can't contain control characters. %s (found at least %s)", py.Repr(host), py.Repr(string(bad))))
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	defaultPort := 80
	if d.Scheme == "https" {
		defaultPort = 443
	}
	if d.Port != defaultPort {
		host += ":" + strconv.Itoa(d.Port)
	}
	return host, nil
}

func (f *Fetcher) hop(ctx context.Context, deadline time.Time, url string, parts py.URLParts, dest Dest, lim Limits) (next string, page Page, err error) {
	path := parts.Path
	if path == "" {
		path = "/"
	}
	target := py.URLUnsplit("", "", path, parts.Query, "")
	if i := strings.IndexFunc(target, isControl); i >= 0 {
		bad, _ := utf8.DecodeRuneInString(target[i:])
		return "", Page{}, FetchError(fmt.Sprintf("URL can't contain control characters. %s (found at least %s)", py.Repr(target), py.Repr(string(bad))))
	}
	requestLine := "GET " + target + " HTTP/1.1"
	if _, err := py.ASCIIEncode(requestLine); err != nil {
		return "", Page{}, err
	}
	host, err := hostHeader(dest)
	if err != nil {
		return "", Page{}, err
	}
	connect := f.Connect
	if connect == nil {
		connect = f.dial
	}
	opTimeout := max(time.Until(deadline), 10*time.Millisecond)
	bounded, cancelBound := context.WithDeadline(ctx, deadline)
	raw, err := connect(bounded, dest)
	cancelBound()
	if err != nil {
		return "", Page{}, connectError(ctx, err)
	}
	defer func() { _ = raw.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stop()
	conn := &opConn{Conn: raw, timeout: opTimeout}
	request := requestLine + "\r\nHost: " + host + "\r\nuser-agent: " + lim.UserAgent + "\r\naccept: " + strings.Join(textTypes, ", ") +
		"\r\naccept-encoding: identity\r\n\r\n"
	if _, err := conn.Write([]byte(request)); err != nil {
		return "", Page{}, connectError(ctx, err)
	}
	resp, err := readResponse(bufio.NewReaderSize(conn, ReadBlock))
	if err != nil {
		return "", Page{}, connectError(ctx, err)
	}
	if location, _ := resp.headers.joined("location"); location != "" && slices.Contains(redirectStatuses, resp.status) {
		next, err := py.URLJoin(url, location)
		if err != nil {
			return "", Page{}, err
		}
		return next, Page{}, nil
	}
	if resp.status != 200 {
		return "", Page{}, FetchError(fmt.Sprintf("status %d", resp.status))
	}
	contentType, ok := resp.headers.joined("content-type")
	if !ok || contentType == "" {
		contentType = "text/html"
	}
	kind, _, _ := strings.Cut(contentType, ";")
	kind = py.Lower(py.Strip(kind))
	if !slices.Contains(textTypes, kind) {
		return "", Page{}, FetchError("content type " + kind)
	}
	if declared, ok := resp.headers.joined("content-length"); ok && declared != "" && isDigits(declared) {
		if n, _ := pyInt(declared, 10); n > int64(lim.MaxBytes) {
			return "", Page{}, FetchError("too large")
		}
	}
	body, err := readBody(resp, deadline, lim.MaxBytes)
	if err != nil {
		return "", Page{}, connectError(ctx, err)
	}
	return "", Page{Body: body, Charset: contentCharset(resp.headers)}, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func connectError(ctx context.Context, err error) error {
	var (
		fe FetchError
		bl Blocked
		pe *py.Exception
	)
	switch {
	case errors.As(err, &fe), errors.As(err, &bl), errors.As(err, &pe):
		return err
	case errors.Is(ctx.Err(), context.Canceled):
		return ctx.Err()
	}
	return FetchError(osText(err))
}

type opConn struct {
	net.Conn
	timeout time.Duration
}

func (c *opConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(c.timeout))
	return c.Conn.Read(p)
}

func (c *opConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(c.timeout))
	return c.Conn.Write(p)
}

func readBody(resp *response, deadline time.Time, maxBytes int) ([]byte, error) {
	var body []byte
	block := make([]byte, ReadBlock)
	for {
		if time.Until(deadline) <= 0 {
			return nil, FetchError("timeout")
		}
		n, err := resp.read(block)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return body, nil
		}
		body = append(body, block[:n]...)
		if len(body) > maxBytes {
			return nil, FetchError("too large")
		}
	}
}
