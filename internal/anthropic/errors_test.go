package anthropic_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/config"
)

func TestNewWithoutKeyIsNotConfigured(t *testing.T) {
	for _, env := range []map[string]string{{}, {"ANTHROPIC_API_KEY": "  "}} {
		_, err := anthropic.New(config.Map(env))
		var nc config.NotConfigured
		if !errors.As(err, &nc) || err.Error() != "ANTHROPIC_API_KEY is not set" {
			t.Errorf("New(%v) = %v, want NotConfigured", env, err)
		}
	}
}

func apiError(t *testing.T, err error) *anthropic.APIError {
	t.Helper()
	var apiErr *anthropic.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T %v is not an APIError", err, err)
	}
	return apiErr
}

func TestStatusErrorsCarryPythonClassAndMessage(t *testing.T) {
	cases := []struct {
		resp        anthropictest.Response
		class, msg  string
		rate, overl bool
	}{
		{anthropictest.Failure(http.StatusTooManyRequests, "rate_limit_error", "slow down"), anthropic.ClassRateLimit,
			"Error code: 429 - {'type': 'error', 'error': {'type': 'rate_limit_error', 'message': 'slow down'}}", true, false},
		{anthropictest.Failure(529, "overloaded_error", "Overloaded"), anthropic.ClassOverloaded,
			"Error code: 529 - {'type': 'error', 'error': {'type': 'overloaded_error', 'message': 'Overloaded'}}", false, true},
		{anthropictest.Failure(http.StatusBadRequest, "invalid_request_error", "container 'c1' expired"), anthropic.ClassBadRequest,
			`Error code: 400 - {'type': 'error', 'error': {'type': 'invalid_request_error', 'message': "container 'c1' expired"}}`, false, false},
		{anthropictest.Failure(http.StatusInternalServerError, "api_error", "boom"), anthropic.ClassInternalServer,
			"Error code: 500 - {'type': 'error', 'error': {'type': 'api_error', 'message': 'boom'}}", false, false},
		{anthropictest.StreamFailure("overloaded_error", "Overloaded"), anthropic.ClassStatus,
			"{'type': 'error', 'error': {'type': 'overloaded_error', 'message': 'Overloaded'}}", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.class, func(t *testing.T) {
			srv := anthropictest.New(t, tc.resp)
			_, err := srv.Client().Stream(context.Background(), chatParams(), nil)
			e := apiError(t, err)
			if e.Class != tc.class || e.Error() != tc.msg || e.RateLimited() != tc.rate || e.Overloaded() != tc.overl {
				t.Errorf("got %s %q rate=%v overloaded=%v", e.Class, e.Error(), e.RateLimited(), e.Overloaded())
			}
		})
	}
}

func TestCreateStatusError(t *testing.T) {
	srv := anthropictest.New(t, anthropictest.Failure(http.StatusUnauthorized, "authentication_error", "invalid x-api-key"))
	_, err := srv.Client().Create(context.Background(), chatParams())
	if e := apiError(t, err); e.Class != anthropic.ClassAuthentication || e.StatusCode != http.StatusUnauthorized || e.Type != "authentication_error" {
		t.Errorf("got %+v", e)
	}
}

func TestConnectionError(t *testing.T) {
	srv := anthropictest.New(t)
	c := srv.Client()
	srv.Close()
	_, err := c.Create(context.Background(), chatParams())
	if e := apiError(t, err); e.Class != anthropic.ClassConnection || e.Error() != anthropic.ConnectionMessage {
		t.Errorf("got %s %q", e.Class, e.Error())
	}
}

func TestDeleteFile(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	srv := anthropictest.New(t)
	c := srv.Client()
	c.DeleteFile(context.Background(), "")
	c.DeleteFile(context.Background(), "file_01")
	srv.FileDeleteStatus = http.StatusNotFound
	c.DeleteFile(context.Background(), "file_02")
	var nilClient *anthropic.Client
	nilClient.DeleteFile(context.Background(), "file_03")

	reqs := srv.Requests()
	if len(reqs) != 2 || reqs[0].Method != http.MethodDelete || reqs[0].Path != "/v1/files/file_01" || reqs[1].Path != "/v1/files/file_02" {
		t.Fatalf("requests %+v", reqs)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "Files API delete of file_02 failed: Error code: 404 - {'type': 'error'") {
		t.Errorf("log %q", logs.String())
	}
}
