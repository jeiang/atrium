package smtp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/jeiang/atrium/internal/config"
)

// SendError is smtp.SendError: every failure of a send, worded as Python's str() of what smtplib raised.
type SendError string

func (e SendError) Error() string { return string(e) }

// ResponseError is smtplib's SMTPResponseException family (SMTPConnectError, SMTPHeloError,
// SMTPAuthenticationError, SMTPDataError): str() is the (code, b'reply') tuple, or (code, 'reply') for the
// few smtplib raises with a str.
type ResponseError struct {
	Kind string
	Code int
	Msg  string
	text bool
}

func (e *ResponseError) Error() string {
	if e.text {
		return fmt.Sprintf("(%d, %s)", e.Code, config.PyRepr(e.Msg))
	}
	return fmt.Sprintf("(%d, %s)", e.Code, pyBytesRepr(e.Msg))
}

// SenderRefused is SMTPSenderRefused(code, reply, sender).
type SenderRefused struct {
	Code   int
	Msg    string
	Sender string
}

func (e *SenderRefused) Error() string {
	return fmt.Sprintf("(%d, %s, %s)", e.Code, pyBytesRepr(e.Msg), config.PyRepr(e.Sender))
}

// RecipientsRefused is SMTPRecipientsRefused: every recipient was refused, so nothing was sent.
type RecipientsRefused struct{ Refused Refusals }

func (e *RecipientsRefused) Error() string {
	parts := make([]string, len(e.Refused))
	for i, r := range e.Refused {
		parts[i] = config.PyRepr(r.Addr) + ": " + r.Reply()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Failure is an smtplib exception whose str() is its message: SMTPServerDisconnected, SMTPNotSupportedError
// and plain SMTPException.
type Failure struct {
	Kind string
	Msg  string
}

func (e *Failure) Error() string { return e.Msg }

// OSError is Python's OSError family as smtplib lets it through: "[Errno N] text" with an errno, the bare
// text without one (TimeoutError("timed out")).
type OSError struct {
	Kind     string
	Errno    int
	HasErrno bool
	Strerror string
}

func (e *OSError) Error() string {
	if e.HasErrno {
		return fmt.Sprintf("[Errno %d] %s", e.Errno, e.Strerror)
	}
	return e.Strerror
}

// Refusal is one entry of the per-recipient refusal map smtplib returns.
type Refusal struct {
	Addr string
	Code int
	Msg  string
}

// Reply is str() of the refusal's (code, b'reply') tuple.
func (r Refusal) Reply() string { return fmt.Sprintf("(%d, %s)", r.Code, pyBytesRepr(r.Msg)) }

// Refusals keeps a Python dict's order: a repeated address keeps its first position and takes the last reply.
type Refusals []Refusal

func (rs Refusals) set(r Refusal) Refusals {
	for i := range rs {
		if rs[i].Addr == r.Addr {
			rs[i] = r
			return rs
		}
	}
	return append(rs, r)
}

const (
	disconnected = "SMTPServerDisconnected"
	notSupported = "SMTPNotSupportedError"
)

func linuxErrno(errno syscall.Errno) (int, string) {
	switch errno {
	case syscall.EPIPE:
		return 32, "Broken pipe"
	case syscall.EADDRNOTAVAIL:
		return 99, "Cannot assign requested address"
	case syscall.ENETUNREACH:
		return 101, "Network is unreachable"
	case syscall.ECONNABORTED:
		return 103, "Software caused connection abort"
	case syscall.ECONNRESET:
		return 104, "Connection reset by peer"
	case syscall.ETIMEDOUT:
		return 110, "Connection timed out"
	case syscall.ECONNREFUSED:
		return 111, "Connection refused"
	case syscall.EHOSTUNREACH:
		return 113, "No route to host"
	}
	text := errno.Error()
	if text != "" {
		text = strings.ToUpper(text[:1]) + text[1:]
	}
	return int(errno), text
}

// osError renders a Go network error as the OSError Python raises for the same condition.
func osError(err error) error {
	var dns *net.DNSError
	var errno syscall.Errno
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return &OSError{Kind: "gaierror", Errno: -2, HasErrno: true, Strerror: "Name or service not known"}
	case errors.As(err, &dns) && !dns.IsTimeout:
		return &OSError{Kind: "gaierror", Errno: -3, HasErrno: true, Strerror: "Temporary failure in name resolution"}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return &OSError{Kind: "TimeoutError", Strerror: "timed out"}
	case errors.As(err, &errno):
		n, text := linuxErrno(errno)
		return &OSError{Kind: "OSError", Errno: n, HasErrno: true, Strerror: text}
	}
	return err
}

func pyBytesRepr(s string) string {
	quote := byte('\'')
	if strings.IndexByte(s, '\'') >= 0 && strings.IndexByte(s, '"') < 0 {
		quote = '"'
	}
	var b strings.Builder
	b.WriteString("b")
	b.WriteByte(quote)
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '\\' || c == quote:
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(quote)
	return b.String()
}

// ASCIIEncodeError is the UnicodeEncodeError s.encode('ascii') raises, nil when s is ASCII.
func ASCIIEncodeError(s string) error {
	rs := []rune(s)
	for i, r := range rs {
		if r < 0x80 {
			continue
		}
		j := i
		for j+1 < len(rs) && rs[j+1] >= 0x80 {
			j++
		}
		if j > i {
			return &Failure{Kind: "UnicodeEncodeError", Msg: fmt.Sprintf("'ascii' codec can't encode characters in position %d-%d: ordinal not in range(128)", i, j)}
		}
		var char string
		switch {
		case r <= 0xff:
			char = fmt.Sprintf(`\x%02x`, r)
		case r <= 0xffff:
			char = fmt.Sprintf(`\u%04x`, r)
		default:
			char = fmt.Sprintf(`\U%08x`, r)
		}
		return &Failure{Kind: "UnicodeEncodeError", Msg: fmt.Sprintf("'ascii' codec can't encode character '%s' in position %d: ordinal not in range(128)", char, i)}
	}
	return nil
}
