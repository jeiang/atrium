package calendar

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

type Error string

func (e Error) Error() string { return string(e) }

func (e Error) PyClass() string { return "CalendarError" }

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func (e NotConfigured) PyClass() string { return "NotConfigured" }

func (e NotConfigured) Unwrap() error { return Error(string(e)) }

type Remote struct {
	Class string
	Text  string
}

func (e *Remote) Error() string { return e.Text }

func (e *Remote) PyClass() string { return e.Class }

func remote(err error) error {
	var api *googleapi.Error
	var refused *oauth2.RetrieveError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &api):
		reason := api.Message
		if reason == "" {
			reason = "Unknown"
		}
		return &Remote{Class: "HttpError", Text: fmt.Sprintf("<HttpError %d returned %q>", api.Code, reason)}
	case errors.As(err, &refused):
		return &Remote{Class: "RefreshError", Text: fmt.Sprintf("(%q, %s)", refreshReason(refused), strings.TrimSpace(string(refused.Body)))}
	}
	var dns *net.DNSError
	var timeout interface{ Timeout() bool }
	var errno syscall.Errno
	switch {
	case errors.As(err, &dns) && dns.IsNotFound:
		return &Remote{Class: "gaierror", Text: "Name or service not known"}
	case errors.As(err, &dns) && !dns.IsTimeout:
		return &Remote{Class: "gaierror", Text: "Temporary failure in name resolution"}
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded),
		errors.As(err, &timeout) && timeout.Timeout():
		return &Remote{Class: "TimeoutError", Text: "timed out"}
	case errors.As(err, &errno):
		return &Remote{Class: "OSError", Text: errno.Error()}
	}
	return &Remote{Class: "Exception", Text: err.Error()}
}

func refreshReason(err *oauth2.RetrieveError) string {
	if err.ErrorCode != "" {
		return err.ErrorCode + ": " + err.ErrorDescription
	}
	return fmt.Sprintf("HTTP %d", err.Response.StatusCode)
}
