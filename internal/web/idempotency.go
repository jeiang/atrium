package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	IdempotencyHeader = "Idempotency-Key"
	idempotencyTTL    = 10 * time.Minute
	idempotencyKeyMax = 255
)

type idemState uint8

const (
	idemNew idemState = iota
	idemReplay
	idemBusy
	idemMismatch
)

type idemEntry struct {
	fingerprint [32]byte
	done        bool
	status      int
	header      http.Header
	body        []byte
	expires     time.Time
}

type idemCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]*idemEntry
}

func newIdemCache() *idemCache {
	return &idemCache{now: time.Now, entries: map[string]*idemEntry{}}
}

func (c *idemCache) begin(id string, fingerprint [32]byte) (idemEntry, idemState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
		}
	}
	if e, ok := c.entries[id]; ok {
		switch {
		case e.fingerprint != fingerprint:
			return idemEntry{}, idemMismatch
		case !e.done:
			return idemEntry{}, idemBusy
		}
		return *e, idemReplay
	}
	c.entries[id] = &idemEntry{fingerprint: fingerprint, expires: now.Add(idempotencyTTL)}
	return idemEntry{}, idemNew
}

func (c *idemCache) finish(id string, status int, header http.Header, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[id]; ok {
		e.done, e.status, e.header, e.body = true, status, header, body
	}
}

func (c *idemCache) drop(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}

type idemCacheKey struct{}

func withIdempotency(ctx context.Context, c *idemCache) context.Context {
	return context.WithValue(ctx, idemCacheKey{}, c)
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

func Idempotent(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.Header.Get(IdempotencyHeader))
		cache, _ := r.Context().Value(idemCacheKey{}).(*idemCache)
		who, known := IdentityOf(r.Context())
		if key == "" || cache == nil || !known {
			h(w, r)
			return
		}
		if len(key) > idempotencyKeyMax {
			APIError(w, http.StatusUnprocessableEntity, "invalid", "The idempotency key is too long.",
				FieldError{Type: "string_too_long", Loc: []string{"header", IdempotencyHeader}, Msg: "At most 255 characters"})
			return
		}
		body, err := io.ReadAll(r.Body)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			APIError(w, http.StatusRequestEntityTooLarge, "too_large", "Request too large.")
			return
		case err != nil:
			APIError(w, http.StatusBadRequest, "bad_request", "The request body could not be read.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		fingerprint := sha256.Sum256([]byte(r.Method + "\x00" + r.URL.Path + "\x00" + string(body)))
		id := who.Subject + "\x00" + key
		entry, state := cache.begin(id, fingerprint)
		switch state {
		case idemReplay:
			for name, values := range entry.header {
				w.Header()[name] = values
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(entry.status)
			_, _ = w.Write(entry.body)
			return
		case idemBusy:
			APIError(w, http.StatusConflict, "idempotency_in_progress", "The first request with this key is still running.")
			return
		case idemMismatch:
			APIError(w, http.StatusUnprocessableEntity, "idempotency_mismatch", "This key was used for a different request.")
			return
		}
		rec := &recorder{ResponseWriter: w}
		finished := false
		defer func() {
			if !finished {
				cache.drop(id)
			}
		}()
		h(rec, r)
		finished = true
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		kept := http.Header{}
		for _, name := range []string{"Content-Type", "Location"} {
			if values := w.Header().Values(name); len(values) > 0 {
				kept[name] = values
			}
		}
		cache.finish(id, status, kept, bytes.Clone(rec.body.Bytes()))
	}
}
