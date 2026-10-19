package media

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type httpFailure struct{ class string }

func (e *httpFailure) Error() string { return e.class }

func newClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: timeout}).DialContext,
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

type reply struct {
	status int
	body   []byte
}

func send(ctx context.Context, client *http.Client, req *http.Request) (reply, error) {
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return reply{}, ctx.Err()
		}
		return reply{}, &httpFailure{errorClass(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if ctx.Err() != nil {
			return reply{}, ctx.Err()
		}
		return reply{}, &httpFailure{errorClass(err)}
	}
	return reply{status: resp.StatusCode, body: body}, nil
}

func errorClass(err error) string {
	var op *net.OpError
	var cert x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	var dns *net.DNSError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "RemoteProtocolError"
	case strings.Contains(err.Error(), "TLS handshake timeout"):
		return "ConnectTimeout"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "ConnectTimeout"
		}
		return "ConnectError"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "ConnectError"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "ReadTimeout"
	}
	return "ReadError"
}

type param struct{ key, value string }

func encodeParams(params []param) string {
	var b strings.Builder
	for i, p := range params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.value))
	}
	return b.String()
}
