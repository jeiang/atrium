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

const (
	spoolFreeFloor = 256 << 20
	envelopeMax    = 65 << 20
	plainBodyMax   = 1 << 20
	uploadIdle     = 30 * time.Second
)

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

type idleBody struct {
	io.ReadCloser
	controller *http.ResponseController
	until      time.Time
	done       bool
}

func (b *idleBody) extend() error {
	b.until = time.Now().Add(uploadIdle)
	return b.controller.SetReadDeadline(b.until)
}

func (b *idleBody) lift() {
	if !b.done {
		b.done = true
		_ = b.controller.SetReadDeadline(time.Time{})
	}
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	switch {
	case b.done:
	case err != nil:
		b.lift()
	case n > 0 && time.Until(b.until) < uploadIdle-time.Second:
		_ = b.extend()
	}
	return n, err
}

func carriesFiles(r *http.Request) bool {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return true
	}
	var prefix bytes.Buffer
	body := r.Body
	defer func() {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(&prefix, body), body}
	}()
	reader := multipart.NewReader(io.TeeReader(body, &prefix), params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil || part.FileName() != "" {
			return true
		}
		if _, err := io.Copy(io.Discard, io.LimitReader(part, int64(plainBodyMax-prefix.Len()+1))); err != nil || prefix.Len() > plainBodyMax {
			return true
		}
	}
}

func scoped(ctx context.Context, key string) string {
	if key == "" {
		return ""
	}
	who, _ := web.IdentityOf(ctx)
	sum := sha256.Sum256([]byte(who.Source + "\x00" + who.Subject))
	return hex.EncodeToString(sum[:8]) + ":" + key
}

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
		multipartForm := media == "multipart/form-data"
		hasFiles := multipartForm && carriesFiles(r)
		if hasFiles {
			body := &idleBody{ReadCloser: r.Body, controller: http.NewResponseController(w)}
			if err := body.extend(); err != nil {
				web.APIFail(w, err)
				return
			}
			defer body.lift()
			r.Body = body
			select {
			case s.multipartSlots <- struct{}{}:
				defer func() { <-s.multipartSlots }()
			default:
				w.Header().Set("Retry-After", "1")
				web.APIFail(w, &application.Error{Code: application.RateLimited, Message: "Too many concurrent uploads."})
				return
			}
			need := uint64(envelopeMax)
			if r.ContentLength > 0 {
				need = min(uint64(r.ContentLength), need)
			}
			var space unix.Statfs_t
			if err := unix.Statfs(s.tempDir, &space); err != nil {
				web.APIFail(w, err)
				return
			}
			if uint64(space.Bavail)*uint64(space.Bsize) < spoolFreeFloor+need {
				web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable."})
				return
			}
		}
		if key != "" {
			mac, err := s.macKey()
			if err != nil {
				web.APIFail(w, err)
				return
			}
			sum := requestHash(r, pattern, media, mac)
			switch {
			case r.PostForm != nil:
				sum.Write([]byte(r.PostForm.Encode()))
				fingerprint = hex.EncodeToString(sum.Sum(nil))
			case r.Body == nil || r.Body == http.NoBody:
				fingerprint = hex.EncodeToString(sum.Sum(nil))
			default:
				hashed := io.Writer(sum)
				if multipartForm {
					hashed = io.Discard
				}
				spool, ok := s.spool(w, r, hashed)
				if !ok {
					return
				}
				defer func() { _ = spool.Close(); _ = os.Remove(spool.Name()) }()
				if body, ok := r.Body.(*idleBody); ok {
					body.lift()
				}
				r.Body = io.NopCloser(spool)
				if !multipartForm {
					fingerprint = hex.EncodeToString(sum.Sum(nil))
					break
				}
				if fingerprint, err = fingerprintParts(r, sum, spool); err != nil {
					var storage *os.PathError
					if errors.As(err, &storage) {
						web.APIFail(w, err)
						return
					}
					web.APIError(w, http.StatusBadRequest, "bad_request", "Invalid multipart body.")
					return
				}
			}
		}
		rec, fresh, err := s.admit(r.Context(), scoped(r.Context(), key), r.Method, pattern, fingerprint)
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

func (s *Store) spool(w http.ResponseWriter, r *http.Request, sum io.Writer) (*os.File, bool) {
	spool, err := os.CreateTemp(s.tempDir, "command-body-")
	if err != nil {
		web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable.", Cause: err})
		return nil, false
	}
	original := r.Body
	_, err = io.Copy(io.MultiWriter(spool, sum), original)
	_ = original.Close()
	if err == nil {
		_, err = spool.Seek(0, io.SeekStart)
	}
	if err == nil {
		return spool, true
	}
	_ = spool.Close()
	_ = os.Remove(spool.Name())
	var large *http.MaxBytesError
	var storage *os.PathError
	switch {
	case errors.As(err, &large):
		web.APIError(w, http.StatusRequestEntityTooLarge, "too_large", "Request too large.")
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT), errors.As(err, &storage):
		web.APIFail(w, &application.Error{Code: application.Unavailable, Message: "Upload storage is unavailable.", Cause: err})
	default:
		web.APIError(w, http.StatusBadRequest, "bad_request", "The request body could not be read.")
	}
	return nil, false
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

func fingerprintParts(r *http.Request, sum hash.Hash, spool *os.File) (string, error) {
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
	if err := json.NewEncoder(sum).Encode(parts); err != nil {
		return "", err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func requestHash(r *http.Request, pattern, media string, mac []byte) hash.Hash {
	sum := hmac.New(sha256.New, mac)
	for _, part := range []string{r.Method, pattern, r.URL.EscapedPath(), r.URL.Query().Encode(), media} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	return sum
}
