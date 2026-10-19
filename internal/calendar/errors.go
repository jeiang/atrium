package calendar

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

// Error is CalendarError.
type Error string

func (e Error) Error() string { return string(e) }

// PyClass is the Python class name of the error.
func (e Error) PyClass() string { return "CalendarError" }

// NotConfigured is calendar.NotConfigured, a CalendarError: a provider whose settings are missing.
type NotConfigured string

func (e NotConfigured) Error() string { return string(e) }

func (e NotConfigured) PyClass() string { return "NotConfigured" }

// Unwrap makes a NotConfigured match as an Error, as the Python subclass does.
func (e NotConfigured) Unwrap() error { return Error(string(e)) }

// Remote is a failure of a provider's service, carrying the Python exception class that reported it.
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
		return &Remote{Class: "HttpError", Text: fmt.Sprintf("<HttpError %d returned %s>", api.Code, py.Repr(reason))}
	case errors.As(err, &refused):
		return &Remote{Class: "RefreshError", Text: fmt.Sprintf("(%s, %s)", py.Repr(refreshReason(refused)), strings.TrimSpace(string(refused.Body)))}
	}
	if osErr, ok := py.OSErrorOf(err); ok {
		return &Remote{Class: osErr.Kind, Text: osErr.Error()}
	}
	return &Remote{Class: "Exception", Text: err.Error()}
}

func refreshReason(err *oauth2.RetrieveError) string {
	if err.ErrorCode != "" {
		return err.ErrorCode + ": " + err.ErrorDescription
	}
	return fmt.Sprintf("HTTP %d", err.Response.StatusCode)
}
