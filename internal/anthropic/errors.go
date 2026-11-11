package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
)

const (
	ConnectionMessage = "Connection error."
	TimeoutMessage    = "Request timed out or interrupted. This could be due to a network timeout, dropped connection, or request cancellation. See https://docs.anthropic.com/en/api/errors#long-requests for more details."
)

const spendCapHeader = "X-Atrium-Spend-Cap"

type APIError struct {
	StatusCode  int
	Message     string
	spendCapped bool
	unsent      bool
	err         error
}

func (e *APIError) Error() string { return e.Message }

func (e *APIError) Unwrap() error { return e.err }

func (e *APIError) SpendCapped() bool { return e.spendCapped }

func (e *APIError) Unsent() bool { return e.unsent }

func neverConnected(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func classify(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		return statusError(apiErr, err)
	}
	var urlErr *url.Error
	var netErr net.Error
	switch {
	case neverConnected(err):
		return &APIError{Message: ConnectionMessage, unsent: true, err: err}
	case errors.As(err, &netErr) && netErr.Timeout():
		return &APIError{Message: TimeoutMessage, err: err}
	case errors.As(err, &urlErr), errors.As(err, &netErr), errors.Is(err, io.ErrUnexpectedEOF):
		return &APIError{Message: ConnectionMessage, err: err}
	}
	return err
}

func statusError(apiErr *sdk.Error, err error) *APIError {
	out := &APIError{StatusCode: apiErr.StatusCode, err: err}
	if apiErr.Response != nil {
		out.spendCapped = apiErr.Response.Header.Get(spendCapHeader) != ""
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiErr.RawJSON()), &body) == nil && body.Error.Message != "" {
		out.Message = body.Error.Message
		return out
	}
	out.Message = strings.TrimSpace(apiErr.Error())
	return out
}
