package smtp

import (
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

type SendError string

func (e SendError) Error() string { return string(e) }

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

type SenderRefused struct {
	Code   int
	Msg    string
	Sender string
}

func (e *SenderRefused) Error() string {
	return fmt.Sprintf("(%d, %s, %s)", e.Code, py.ReprBytes([]byte(e.Msg)), py.Repr(e.Sender))
}

type RecipientsRefused struct{ Refused Refusals }

func (e *RecipientsRefused) Error() string {
	parts := make([]string, len(e.Refused))
	for i, r := range e.Refused {
		parts[i] = py.Repr(r.Addr) + ": " + r.Reply()
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

type Failure struct {
	Kind string
	Msg  string
}

func (e *Failure) Error() string { return e.Msg }

type Refusal struct {
	Addr string
	Code int
	Msg  string
}

func (r Refusal) Reply() string { return fmt.Sprintf("(%d, %s)", r.Code, py.ReprBytes([]byte(r.Msg))) }

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

func osError(err error) error {
	if e, ok := py.OSErrorOf(err); ok {
		return e
	}
	return err
}
