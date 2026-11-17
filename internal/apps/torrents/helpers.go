package torrents

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/apps/torrents/contract"
)

const (
	TuningMediaQbittorrentTimeoutSeconds = 15
	TuningMediaTorrentUploadBytes        = 1048576
)

type (
	Torrent         = contract.Torrent
	TorrentFile     = contract.TorrentFile
	QbitError       = contract.QbitError
	QbitStatusError = contract.QbitStatusError
	QbitRejected    = contract.QbitRejected
)

type httpFailure struct{ class string }

func (e *httpFailure) Error() string { return e.class }

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
		return "protocol error"
	case errors.As(err, &op) && op.Op == "dial":
		if op.Timeout() {
			return "connect timeout"
		}
		return "connect failed"
	case errors.As(err, &cert), errors.As(err, &host), errors.As(err, &invalid), errors.As(err, &dns):
		return "connect failed"
	}
	var timeouter interface{ Timeout() bool }
	if errors.As(err, &timeouter) && timeouter.Timeout() {
		return "read timeout"
	}
	return "read failed"
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

func truthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func parseInt(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	if body == "" || strings.HasPrefix(body, "_") || strings.HasSuffix(body, "_") || strings.Contains(body, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(body, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}
