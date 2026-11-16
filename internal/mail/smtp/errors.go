package smtp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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

func SessionFailure(err error) (failed, credentials bool) {
	if err == nil || errors.As(err, new(*OutcomeUnknown)) {
		return false, false
	}
	if response, ok := errors.AsType[*ResponseError](err); ok {
		switch response.Kind {
		case "SMTPAuthenticationError":
			return true, response.Code >= 530 && response.Code <= 535
		case "SMTPConnectError", "SMTPHeloError":
			return true, false
		}
		return false, false
	}
	if failure, ok := errors.AsType[*Failure](err); ok {
		return failure.Kind == disconnected, false
	}
	return errors.As(err, new(net.Error)) || errors.As(err, new(*tls.CertificateVerificationError)) ||
		errors.As(err, new(tls.AlertError)) || errors.As(err, new(tls.RecordHeaderError)), false
}
