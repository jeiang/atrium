package commands

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/web"
)

var log = logx.Logger("commands")

var multipartSlots = make(chan struct{}, 4)

const spoolFreeFloor = 256 << 20

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
		media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if media == "multipart/form-data" {
			select {
			case multipartSlots <- struct{}{}:
				defer func() { <-multipartSlots }()
			default:
				w.Header().Set("Retry-After", "1")
				web.APIFail(w, &application.Error{Code: application.RateLimited, Message: "Too many concurrent uploads."})
				return
			}
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(time.Now().Add(2 * time.Minute)); err != nil {
				web.APIFail(w, err)
				return
			}
			defer func() { _ = controller.SetReadDeadline(time.Time{}) }()
			var space unix.Statfs_t
			if err := unix.Statfs(s.tempDir, &space); err != nil {
				web.APIFail(w, err)
				return
			}
			if uint64(space.Bavail)*uint64(space.Bsize) < spoolFreeFloor+65<<20 {
				web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable."})
				return
			}
			if key != "" {
				spool, err := os.CreateTemp(s.tempDir, "command-body-")
				if err != nil {
					web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable.", Cause: err})
					return
				}
				defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
				original := r.Body
				_, err = io.Copy(spool, original)
				_ = original.Close()
				var large *http.MaxBytesError
				var storage *os.PathError
				switch {
				case errors.As(err, &large):
					web.APIError(w, 413, "too_large", "Request too large.")
					return
				case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT), errors.As(err, &storage):
					web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable.", Cause: err})
					return
				case err != nil:
					web.APIError(w, 400, "bad_request", "The request body could not be read.")
					return
				}
				if _, err := spool.Seek(0, io.SeekStart); err != nil {
					web.APIFail(w, err)
					return
				}
				r.Body = io.NopCloser(spool)
				mac, err := s.macKey()
				if err != nil {
					web.APIFail(w, err)
					return
				}
				fingerprint, err = fingerprintStream(r, pattern, mac, spool)
				if err != nil {
					if errors.As(err, &storage) {
						web.APIFail(w, err)
						return
					}
					web.APIError(w, http.StatusBadRequest, "bad_request", "Invalid multipart body.")
					return
				}
			}
		}
		if key != "" && media != "multipart/form-data" {
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
			for name, values := range headers {
				if name == "Content-Type" || name == "Location" || strings.HasPrefix(name, "Hx-") {
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

func fingerprintStream(r *http.Request, pattern string, mac []byte, spool *os.File) (string, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return "", errors.New("invalid multipart boundary")
	}
	type partFingerprint struct {
		Name      string
		Index     int
		Filename  string
		MediaType string
		Digest    string
	}
	var parts []partFingerprint
	indexes := map[string]int{}
	reader := multipart.NewReader(spool, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if len(parts) >= 1000 {
			return "", errors.New("too many multipart parts")
		}
		digest := sha256.New()
		if _, err := io.Copy(digest, part); err != nil {
			return "", err
		}
		name := part.FormName()
		parts = append(parts, partFingerprint{Name: name, Index: indexes[name], Filename: part.FileName(), MediaType: part.Header.Get("Content-Type"), Digest: hex.EncodeToString(digest.Sum(nil))})
		indexes[name]++
	}
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].Name != parts[j].Name {
			return parts[i].Name < parts[j].Name
		}
		return parts[i].Index < parts[j].Index
	})
	sum := requestHash(r, pattern, mac)
	if err := json.NewEncoder(sum).Encode(parts); err != nil {
		return "", err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func requestHash(r *http.Request, pattern string, mac []byte) hash.Hash {
	sum := hmac.New(sha256.New, mac)
	for _, part := range []string{r.Method, pattern, r.URL.EscapedPath(), r.URL.Query().Encode()} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return sum
}

func fingerprintOf(r *http.Request, pattern string, mac []byte) (string, error) {
	sum := requestHash(r, pattern, mac)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	sum.Write(body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	return hex.EncodeToString(sum.Sum(nil)), nil
}
