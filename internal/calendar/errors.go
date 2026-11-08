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

type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func (e NotConfigured) Unwrap() error { return Error(string(e)) }

type Remote struct {
	Text string
}

func (e *Remote) Error() string { return e.Text }

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
		return &Remote{Text: fmt.Sprintf("calendar API %d: %s", api.Code, reason)}
	case errors.As(err, &refused):
		return &Remote{Text: "calendar refresh failed: " + strings.TrimSpace(string(refused.Body))}
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
		return &Remote{Text: "timed out"}
	case errors.As(err, &errno):
		return &Remote{Text: errno.Error()}
	}
	return &Remote{Text: err.Error()}
}
