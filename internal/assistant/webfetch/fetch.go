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

	"github.com/jeiang/atrium/internal/py"
)

const (
	MaxBytes      = 2 * 1024 * 1024
	FetchTimeout  = 10 * time.Second
	MaxRedirects  = 5
	ReadBlock     = 65536
	UserAgent     = "atrium-webfetch/1"
	PageTextChars = 60000
)

var (
	textTypes        = []string{"text/html", "application/xhtml+xml", "text/plain"}
	redirectStatuses = []int{301, 302, 303, 307, 308}
)

// Page is what fetch_page returns: the body, the charset the server declared (utf-8 when none) and the URL the
// last redirect led to.
type Page struct {
	Body    []byte
	Charset string
	URL     string
}

// Dest is one connection to open: the host and port the URL names and the vetted address to connect to instead of
// resolving the host again.
type Dest struct {
	Scheme  string
	Host    string
	Port    int
	Address netip.Addr
}

// Fetcher runs fetch_page. The zero value is the production fetcher; the fields are test seams and the policy of
// the guard (Python's allowed argument).
type Fetcher struct {
	// Allowed vets every address a host resolves to; nil is DefaultAllowed.
	Allowed func(netip.Addr) bool
	// Lookup is getaddrinfo for a stream socket; nil is the system resolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Connect opens the connection for dest, wrapped in TLS for https; nil dials the pinned address.
	Connect func(ctx context.Context, dest Dest) (net.Conn, error)
	// Timeout bounds the whole fetch, redirects included; zero is FetchTimeout.
	Timeout time.Duration

	tlsRoots *x509.CertPool
}

// FetchPage is fetch_page(url) with the production Fetcher.
func FetchPage(ctx context.Context, url string) (Page, error) {
	var f Fetcher
	return f.FetchPage(ctx, url)
}

func (f *Fetcher) allowed(ip netip.Addr) bool {
	if f.Allowed != nil {
		return f.Allowed(ip)
	}
	return DefaultAllowed(ip)
}

// FetchPage is fetch_page: GET url as text, following at most MaxRedirects redirects. Every hop resolves its host
// once, refuses the hop when any answer is not allowed, and connects to the first answer, so the address that was
// vetted is the address that is used.
func (f *Fetcher) FetchPage(ctx context.Context, url string) (Page, error) {
	timeout := f.Timeout
	if timeout == 0 {
		timeout = FetchTimeout
	}
	deadline := time.Now().Add(timeout)
	for range MaxRedirects + 1 {
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
		bounded, cancelBound := context.WithDeadline(ctx, deadline)
		address, err := f.resolve(bounded, host, port)
		cancelBound()
		if err != nil {
			return Page{}, connectError(ctx, err)
		}
		next, page, err := f.hop(ctx, deadline, url, parts, Dest{Scheme: parts.Scheme, Host: host, Port: port, Address: address})
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
	name := host
	if !isASCII(name) {
		ascii, err := idna.Lookup.ToASCII(name)
		if err != nil {
			return nil, py.ValueError("encoding with 'idna' codec failed (UnicodeError: " + err.Error() + ")")
		}
		name = ascii
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
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

// resolve is resolve(host, port, allowed): the first answer, after every answer passed the guard.
func (f *Fetcher) resolve(ctx context.Context, host string, port int) (netip.Addr, error) {
	addrs, err := f.lookup(ctx, host)
	if err != nil {
		var ve py.ValueError
		if errors.As(err, &ve) {
			return netip.Addr{}, err
		}
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

// dial connects to the vetted address, never to the host name, and checks the address again as the socket
// connects.
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
	if !isASCII(host) {
		ascii, err := idna.Lookup.ToASCII(host)
		if err != nil {
			return "", py.ValueError("encoding with 'idna' codec failed (UnicodeError: " + err.Error() + ")")
		}
		host = ascii
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

func (f *Fetcher) hop(ctx context.Context, deadline time.Time, url string, parts py.URLParts, dest Dest) (next string, page Page, err error) {
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
	request := requestLine + "\r\nHost: " + host + "\r\nuser-agent: " + UserAgent + "\r\naccept: " + strings.Join(textTypes, ", ") +
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
		if n, _ := pyInt(declared, 10); n > MaxBytes {
			return "", Page{}, FetchError("too large")
		}
	}
	body, err := readBody(resp, deadline)
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

// connectError is the FetchError for an OSError or HTTPException; errors Python lets through keep their class.
func connectError(ctx context.Context, err error) error {
	var (
		fe  FetchError
		bl  Blocked
		ve  py.ValueError
		uee py.UnicodeEncodeError
	)
	switch {
	case errors.As(err, &fe), errors.As(err, &bl), errors.As(err, &ve), errors.As(err, &uee):
		return err
	case errors.Is(ctx.Err(), context.Canceled):
		return ctx.Err()
	}
	return FetchError(osText(err))
}

// opConn gives every read and write the time the connection had left when it was opened, as the socket timeout of
// http.client does: a stalled peer times out, and a slow one is caught by the deadline check between reads.
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

// readBody is read_body: blocks of ReadBlock bytes until the end, a FetchError past MaxBytes or the deadline.
func readBody(resp *response, deadline time.Time) ([]byte, error) {
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
		if len(body) > MaxBytes {
			return nil, FetchError("too large")
		}
	}
}
