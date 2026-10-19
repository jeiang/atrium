package smtp

import (
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/py"
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
		return fmt.Sprintf("(%d, %s)", e.Code, py.Repr(e.Msg))
	}
	return fmt.Sprintf("(%d, %s)", e.Code, py.ReprBytes([]byte(e.Msg)))
}

// SenderRefused is SMTPSenderRefused(code, reply, sender).
type SenderRefused struct {
	Code   int
	Msg    string
	Sender string
}

func (e *SenderRefused) Error() string {
	return fmt.Sprintf("(%d, %s, %s)", e.Code, py.ReprBytes([]byte(e.Msg)), py.Repr(e.Sender))
}

// RecipientsRefused is SMTPRecipientsRefused: every recipient was refused, so nothing was sent.
type RecipientsRefused struct{ Refused Refusals }

func (e *RecipientsRefused) Error() string {
	parts := make([]string, len(e.Refused))
	for i, r := range e.Refused {
		parts[i] = py.Repr(r.Addr) + ": " + r.Reply()
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

// Refusal is one entry of the per-recipient refusal map smtplib returns.
type Refusal struct {
	Addr string
	Code int
	Msg  string
}

// Reply is str() of the refusal's (code, b'reply') tuple.
func (r Refusal) Reply() string { return fmt.Sprintf("(%d, %s)", r.Code, py.ReprBytes([]byte(r.Msg))) }

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

// osError renders a Go network error as the OSError Python raises for the same condition.
func osError(err error) error {
	if e, ok := py.OSErrorOf(err); ok {
		return e
	}
	return err
}
