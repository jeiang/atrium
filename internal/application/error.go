package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jeiang/atrium/internal/logx"
)

type Code string

const (
	Invalid         Code = "invalid"
	NotFound        Code = "not_found"
	Unauthenticated Code = "unauthenticated"
	Forbidden       Code = "forbidden"
	Conflict        Code = "conflict"
	Refused         Code = "refused"
	NotConfigured   Code = "not_configured"
	RateLimited     Code = "rate_limited"
	Budget          Code = "budget_exceeded"
	Unavailable     Code = "unavailable"
	Unknown         Code = "effect_unknown"
	Partial         Code = "effect_partial"
	Canceled        Code = "canceled"
	Internal        Code = "internal"
)

type Field struct {
	Name    string
	Code    string
	Message string
}

type Error struct {
	Code         Code
	Message      string
	Fields       []Field
	EffectIDs    []int64
	RetryAfter   time.Duration
	DiagnosticID string
	Cause        error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Code)
}

func (e *Error) Unwrap() error { return e.Cause }

const (
	canceledMessage = "The request was canceled."
	internalMessage = "Something went wrong."
)

var log = logx.Logger("application")

func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var classified *Error
	if errors.As(err, &classified) {
		return classified
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: Canceled, Message: canceledMessage, Cause: err}
	}
	id := diagnosticID()
	log.Error("operation failed", "diagnostic_id", id, logx.Err, err)
	return &Error{Code: Internal, Message: internalMessage, DiagnosticID: id, Cause: err}
}

func diagnosticID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
