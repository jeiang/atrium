package commands

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("commands")

type control struct{ withhold bool }

type controlKey struct{}

func Withhold(ctx context.Context) {
	if c, ok := ctx.Value(controlKey{}).(*control); ok {
		c.withhold = true
	}
}

type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Store) Middleware(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(IdempotencyHeader))
		if len(key) > keyMax {
			web.APIError(w, http.StatusUnprocessableEntity, "invalid", "The idempotency key is too long.",
				web.FieldError{Type: "string_too_long", Loc: []string{"header", IdempotencyHeader}, Msg: "At most 255 characters"})
			return
		}
		var fingerprint string
		if key != "" {
			mac, err := s.macKey()
			if err != nil {
				web.APIFail(w, err)
				return
			}
			fingerprint, err = fingerprintOf(r, pattern, mac)
			var tooLarge *http.MaxBytesError
			switch {
			case errors.As(err, &tooLarge):
				web.APIError(w, http.StatusRequestEntityTooLarge, "too_large", "Request too large.")
				return
			case err != nil:
				web.APIError(w, http.StatusBadRequest, "bad_request", "The request body could not be read.")
				return
			}
		}
		rec, fresh, err := s.admit(r.Context(), key, r.Method, pattern, fingerprint)
		if err != nil {
			web.APIFail(w, err)
			return
		}
		if !fresh {
			replay(w, rec, fingerprint)
			return
		}
		w.Header().Set(Header, rec.id)
		ctl := &control{}
		ctx := context.WithValue(r.Context(), controlKey{}, ctl)
		inv, _ := application.InvocationOf(ctx)
		inv.CommandID = rec.id
		ctx = application.WithInvocation(ctx, inv)
		out := &recorder{ResponseWriter: w}
		defer func() {
			p := recover()
			status, body, headers := out.status, out.body.Bytes(), w.Header()
			if p != nil {
				failure := httptest.NewRecorder()
				web.APIError(failure, http.StatusInternalServerError, "internal", "Internal error.")
				status, body, headers = failure.Code, failure.Body.Bytes(), failure.Header()
			}
			if status == 0 {
				status = http.StatusOK
			}
			kept := http.Header{}
			for _, name := range []string{"Content-Type", "Location"} {
				if values := headers.Values(name); len(values) > 0 {
					kept[name] = values
				}
			}
			res := result{keyed: key != "", status: status, headers: kept, body: bytes.Clone(body), withheld: ctl.withhold}
			if err := s.finish(context.WithoutCancel(ctx), rec.id, res); err != nil {
				log.Error("command result not stored", "command_id", rec.id, logx.Err, err.Error())
			}
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(out, r.WithContext(ctx))
	})
}

func replay(w http.ResponseWriter, rec record, fingerprint string) {
	switch {
	case rec.fingerprint != fingerprint:
		web.APIError(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "This key was used for a different request.")
	case rec.state == Running:
		web.APIError(w, http.StatusConflict, "idempotency_in_progress", "The first request with this key is still running.")
	case rec.state == Interrupted:
		w.Header().Set(Header, rec.id)
		web.CommandError(w, http.StatusConflict, "command_interrupted", "The first request with this key was interrupted; it is never run again.", rec.id)
	case rec.withheld:
		w.Header().Set(Header, rec.id)
		web.CommandError(w, http.StatusConflict, "command_result_withheld", "The first request with this key succeeded; its result is shown only once.", rec.id)
	default:
		h := w.Header()
		for name, values := range rec.headers {
			h[name] = values
		}
		h.Set(Header, rec.id)
		h.Set(ReplayedHeader, "true")
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
	}
}

func fingerprintOf(r *http.Request, pattern string, mac []byte) (string, error) {
	sum := hmac.New(sha256.New, mac)
	for _, part := range []string{r.Method, pattern, r.URL.EscapedPath(), r.URL.Query().Encode()} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	sum.Write(body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return hex.EncodeToString(sum.Sum(nil)), nil
}
