package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"

	"github.com/jeiang/atrium/internal/davmirror"
)

type Error string

func (e Error) Error() string { return string(e) }

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func (e NotConfigured) Unwrap() error { return Error(string(e)) }

type Remote struct {
	Text    string
	Unknown bool
}

func (e *Remote) Error() string { return e.Text }

func (e *Remote) OutcomeUnknown() bool { return e.Unknown }

type Partial struct {
	Err error
}

func (e *Partial) Error() string { return e.Err.Error() }

func (e *Partial) Unwrap() error { return e.Err }

func (e *Partial) OutcomeUnknown() bool { return true }

func OutcomeUnknown(err error) bool {
	var remote *Remote
	if errors.As(err, &remote) {
		return remote.Unknown
	}
	var partial *Partial
	return errors.As(err, &partial)
}

func remote(err error) error {
	var api *googleapi.Error
	var refused *oauth2.RetrieveError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &api):
		reason := api.Message
		if reason == "" {
			reason = "unknown"
		}
		return &Remote{Text: fmt.Sprintf("calendar API %d: %s", api.Code, reason), Unknown: api.Code >= 500}
	case errors.As(err, &refused):
		return &Remote{Text: "calendar refresh failed: " + strings.TrimSpace(string(refused.Body))}
	}
	var dav *davmirror.StatusError
	if errors.As(err, &dav) {
		return &Remote{Text: dav.Error(), Unknown: dav.Status >= 500}
	}
	if code := statusCode(err.Error()); code != 0 {
		return &Remote{Text: err.Error(), Unknown: code >= 500}
	}
	var dns *net.DNSError
	var timeout interface{ Timeout() bool }
	var errno syscall.Errno
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return &Remote{Text: "name or service not known"}
	case errors.As(err, &dns) && !dns.IsTimeout:
		return &Remote{Text: "temporary failure in name resolution"}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return &Remote{Text: "timed out", Unknown: true}
	case errors.As(err, &errno):
		unknown := errno == syscall.ECONNRESET || errno == syscall.EPIPE || errno == syscall.ECONNABORTED
		return &Remote{Text: errno.Error(), Unknown: unknown}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) {
		return &Remote{Text: err.Error(), Unknown: true}
	}
	return &Remote{Text: err.Error(), Unknown: true}
}

func statusCode(text string) int {
	if len(text) < 4 || text[3] != ' ' {
		return 0
	}
	code, err := strconv.Atoi(text[:3])
	if err != nil || code < 100 || code > 599 {
		return 0
	}
	return code
}
