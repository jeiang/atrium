// Package httpx is the plain HTTP client of src/atrium/http.py: JSON POSTs
// to the local model servers and text GETs, with every transport failure
// reported as ServiceUnavailable.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PostTimeout = 600 * time.Second
	GetTimeout  = 30 * time.Second
	UserAgent   = "atrium/1 (ledger rate fetch)"
)

// Python exception classes that ported except clauses tell apart; errors
// from malformed replies wrap one of them.
var (
	ErrValue = errors.New("ValueError")
	ErrKey   = errors.New("KeyError")
	ErrIndex = errors.New("IndexError")
	ErrType  = errors.New("TypeError")
)

// ServiceUnavailable is Python's http.ServiceUnavailable: no URL, a
// transport failure, a timeout or a non-2xx status.
type ServiceUnavailable struct{ Msg string }

func (e *ServiceUnavailable) Error() string { return e.Msg }

var client = &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if req.Method == http.MethodPost {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}}

// PostJSON posts payload as JSON and returns the JSON reply; a reply that
// is not JSON is an ErrValue.
func PostJSON(ctx context.Context, rawURL string, payload any) (json.RawMessage, error) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, &ServiceUnavailable{"no service URL configured for " + pyRepr(rawURL)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	reply, err := do(ctx, rawURL, PostTimeout, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return nil, err
	}
	if !json.Valid(reply) {
		return nil, fmt.Errorf("%w: %s: reply is not JSON", ErrValue, rawURL)
	}
	return reply, nil
}

// GetText fetches rawURL and decodes it as UTF-8 with Python's "replace"
// error handler; timeout <= 0 means GetTimeout.
func GetText(ctx context.Context, rawURL string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = GetTimeout
	}
	reply, err := do(ctx, rawURL, timeout, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err == nil {
			req.Header.Set("User-Agent", UserAgent)
		}
		return req, err
	})
	if err != nil {
		return "", err
	}
	return DecodeReplace(reply), nil
}

func do(parent context.Context, rawURL string, timeout time.Duration, build func(context.Context) (*http.Request, error)) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	req, err := build(ctx)
	if err != nil {
		return nil, &ServiceUnavailable{fmt.Sprintf("%s: <urlopen error %v>", rawURL, err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, &ServiceUnavailable{fmt.Sprintf("%s: <urlopen error %s>", rawURL, reason(err))}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		phrase := strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)+" ")
		return nil, &ServiceUnavailable{fmt.Sprintf("%s: HTTP Error %d: %s", rawURL, resp.StatusCode, phrase)}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		if parent.Err() != nil {
			return nil, parent.Err()
		}
		return nil, &ServiceUnavailable{fmt.Sprintf("%s: %s", rawURL, reason(err))}
	}
	return data, nil
}

func reason(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timed out"
		}
		err = ue.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

// DecodeReplace is Python's bytes.decode("utf-8", "replace"): each maximal
// invalid subpart becomes one U+FFFD.
func DecodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b))
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r != utf8.RuneError || n > 1 {
			out.Write(b[:n])
			b = b[n:]
			continue
		}
		out.WriteRune(utf8.RuneError)
		b = b[invalidPrefix(b):]
	}
	return out.String()
}

func invalidPrefix(b []byte) int {
	var need int
	lo, hi := byte(0x80), byte(0xBF)
	switch c := b[0]; {
	case c >= 0xC2 && c <= 0xDF:
		need = 1
	case c >= 0xE0 && c <= 0xEF:
		need = 2
		switch c {
		case 0xE0:
			lo = 0xA0
		case 0xED:
			hi = 0x9F
		}
	case c >= 0xF0 && c <= 0xF4:
		need = 3
		switch c {
		case 0xF0:
			lo = 0x90
		case 0xF4:
			hi = 0x8F
		}
	default:
		return 1
	}
	n := 1
	for ; n <= need && n < len(b); n++ {
		if b[n] < lo || b[n] > hi {
			break
		}
		lo, hi = 0x80, 0xBF
	}
	return n
}

// pyRepr is Python's repr of a str.
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == rune(quote):
			b.WriteByte('\\')
			b.WriteByte(quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}
