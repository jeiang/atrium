package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

const (
	ConnectionMessage = "Connection error."
	TimeoutMessage    = "Request timed out or interrupted. This could be due to a network timeout, dropped connection, or request cancellation. See https://docs.anthropic.com/en/api/errors#long-requests for more details."
)

const (
	ClassStatus          = "APIStatusError"
	ClassConnection      = "APIConnectionError"
	ClassTimeout         = "APITimeoutError"
	ClassBadRequest      = "BadRequestError"
	ClassAuthentication  = "AuthenticationError"
	ClassPermission      = "PermissionDeniedError"
	ClassNotFound        = "NotFoundError"
	ClassConflict        = "ConflictError"
	ClassRequestTooLarge = "RequestTooLargeError"
	ClassUnprocessable   = "UnprocessableEntityError"
	ClassRateLimit       = "RateLimitError"
	ClassOverloaded      = "OverloadedError"
	ClassInternalServer  = "InternalServerError"
)

type APIError struct {
	Class      string
	StatusCode int
	Type       string
	Message    string
	RetryAfter string
	err        error
}

func (e *APIError) Error() string { return e.Message }

func (e *APIError) Unwrap() error { return e.err }

func (e *APIError) PyClass() string { return e.Class }

func (e *APIError) RateLimited() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.Type == "rate_limit_error"
}

func (e *APIError) SpendCapped() bool {
	return e.StatusCode == http.StatusTooManyRequests && e.RetryAfter == ""
}

func (e *APIError) Overloaded() bool { return e.StatusCode == 529 || e.Type == "overloaded_error" }

func statusClass(status int) string {
	switch status {
	case http.StatusBadRequest:
		return ClassBadRequest
	case http.StatusUnauthorized:
		return ClassAuthentication
	case http.StatusForbidden:
		return ClassPermission
	case http.StatusNotFound:
		return ClassNotFound
	case http.StatusConflict:
		return ClassConflict
	case http.StatusRequestEntityTooLarge:
		return ClassRequestTooLarge
	case http.StatusUnprocessableEntity:
		return ClassUnprocessable
	case http.StatusTooManyRequests:
		return ClassRateLimit
	case 529:
		return ClassOverloaded
	}
	if status >= 500 {
		return ClassInternalServer
	}
	return ClassStatus
}

func classify(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		return statusError(apiErr, err)
	}
	var urlErr *url.Error
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return &APIError{Class: ClassTimeout, Message: TimeoutMessage, err: err}
	case errors.As(err, &urlErr), errors.As(err, &netErr), errors.Is(err, io.ErrUnexpectedEOF):
		return &APIError{Class: ClassConnection, Message: ConnectionMessage, err: err}
	}
	return err
}

func statusError(apiErr *sdk.Error, err error) *APIError {
	status := apiErr.StatusCode
	text := strings.TrimSpace(apiErr.RawJSON())
	out := &APIError{Class: statusClass(status), StatusCode: status, Type: string(apiErr.Type()), err: err}
	if apiErr.Response != nil {
		out.RetryAfter = strings.TrimSpace(apiErr.Response.Header.Get("Retry-After"))
	}
	repr, ok := pyRepr([]byte(text))
	switch {
	case ok && status < http.StatusBadRequest:
		out.Message = repr
	case ok:
		out.Message = fmt.Sprintf("Error code: %d - %s", status, repr)
	case text != "":
		out.Message = text
	default:
		out.Message = fmt.Sprintf("Error code: %d", status)
	}
	return out
}

func pyRepr(data []byte) (string, bool) {
	if !json.Valid(data) {
		return "", false
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s, true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var b strings.Builder
	if err := reprValue(dec, &b); err != nil {
		return "", false
	}
	return b.String(), true
}

func reprValue(dec *json.Decoder, b *strings.Builder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		closer, sep := "]", ", "
		if t == '{' {
			closer = "}"
		}
		b.WriteString(t.String())
		for i := 0; dec.More(); i++ {
			if i > 0 {
				b.WriteString(sep)
			}
			if t == '{' {
				key, err := dec.Token()
				if err != nil {
					return err
				}
				b.WriteString(reprString(key.(string)))
				b.WriteString(": ")
			}
			if err := reprValue(dec, b); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		b.WriteString(closer)
	case string:
		b.WriteString(reprString(t))
	case json.Number:
		b.WriteString(reprNumber(t))
	case bool:
		if t {
			b.WriteString("True")
		} else {
			b.WriteString("False")
		}
	case nil:
		b.WriteString("None")
	}
	return nil
}

func reprString(s string) string {
	quote := byte('\'')
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteByte(quote)
	for _, r := range s {
		switch {
		case r == '\\' || r == rune(quote):
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == utf8.RuneError || strconv.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

func reprNumber(n json.Number) string {
	s := n.String()
	if !strings.ContainsAny(s, ".eE") {
		if i, ok := new(big.Int).SetString(s, 10); ok {
			return i.String()
		}
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return reprFloat(f)
}

func reprFloat(f float64) string {
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, expText, _ := strings.Cut(sci, "e")
	exp, _ := strconv.Atoi(expText)
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign, mantissa = "-", mantissa[1:]
	}
	digits := strings.Replace(mantissa, ".", "", 1)
	if exp < -4 || exp >= 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		es := "+"
		if exp < 0 {
			es, exp = "-", -exp
		}
		return fmt.Sprintf("%s%se%s%02d", sign, m, es, exp)
	}
	point := exp + 1
	switch {
	case point <= 0:
		return sign + "0." + strings.Repeat("0", -point) + digits
	case point >= len(digits):
		return sign + digits + strings.Repeat("0", point-len(digits)) + ".0"
	}
	return sign + digits[:point] + "." + digits[point:]
}
