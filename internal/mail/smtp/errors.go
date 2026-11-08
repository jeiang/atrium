package smtp

import (
	"fmt"
	"strings"
)

type OutcomeUnknown struct{ Err error }

func (e *OutcomeUnknown) Error() string { return e.Err.Error() }

func (e *OutcomeUnknown) Unwrap() error { return e.Err }

type ResponseError struct {
	Kind string
	Code int
	Msg  string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("SMTP %d: %s", e.Code, e.Msg)
}

type SenderRefused struct {
	Code   int
	Msg    string
	Sender string
}

func (e *SenderRefused) Error() string {
	return fmt.Sprintf("SMTP %d: sender %s refused: %s", e.Code, e.Sender, e.Msg)
}

type RecipientsRefused struct{ Refused Refusals }

func (e *RecipientsRefused) Error() string {
	parts := make([]string, len(e.Refused))
	for i, r := range e.Refused {
		parts[i] = r.Addr + ": " + r.Reply()
	}
	return strings.Join(parts, ", ")
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

func (r Refusal) Reply() string { return fmt.Sprintf("SMTP %d: %s", r.Code, r.Msg) }

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
	return err
}
