package web

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/jeiang/atrium/internal/application"
)

func Status(code application.Code) int {
	switch code {
	case application.Invalid:
		return http.StatusUnprocessableEntity
	case application.NotFound:
		return http.StatusNotFound
	case application.Unauthenticated:
		return http.StatusUnauthorized
	case application.Forbidden:
		return http.StatusForbidden
	case application.Refused, application.Conflict, application.Budget, application.Unknown, application.Partial:
		return http.StatusConflict
	case application.NotConfigured, application.Unavailable:
		return http.StatusServiceUnavailable
	case application.RateLimited:
		return http.StatusTooManyRequests
	case application.Canceled:
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

func Fail(w http.ResponseWriter, r *http.Request, err error, render func(status int)) {
	e := application.Classify(err)
	if e.Code == application.Canceled && errors.Is(r.Context().Err(), context.Canceled) {
		return
	}
	failHeaders(w, e)
	render(Status(e.Code))
}

func Message(e *application.Error) string {
	if e.DiagnosticID == "" {
		return e.Error()
	}
	return e.Error() + " Diagnostic ID: " + e.DiagnosticID + "."
}

func APIFail(w http.ResponseWriter, err error) {
	e := application.Classify(err)
	failHeaders(w, e)
	if e.Code == application.Unauthenticated {
		w.Header().Set("WWW-Authenticate", realm)
	}
	body := apiError{Code: string(e.Code), Message: e.Error(), EffectIDs: e.EffectIDs, DiagnosticID: e.DiagnosticID}
	if e.RetryAfter > 0 {
		body.RetryAfter = retryAfterSeconds(e)
	}
	for _, f := range e.Fields {
		body.Fields = append(body.Fields, FieldError{Type: f.Code, Loc: []string{"body", f.Name}, Msg: f.Message})
	}
	JSON(w, Status(e.Code), map[string]apiError{"error": body})
}

func failHeaders(w http.ResponseWriter, e *application.Error) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds(e), 10))
	}
}

func retryAfterSeconds(e *application.Error) int64 {
	return int64(math.Ceil(e.RetryAfter.Seconds()))
}
