package web

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/jeiang/atrium/internal/application"
)

const (
	RunStreamsPerSubject = 4
	RunStreamsPerServer  = 16
	AppStreamsPerSubject = 2
	AppStreamsPerServer  = 8
)

var streamCaps = map[application.StreamKind]struct{ subject, server int }{
	application.RunStream: {RunStreamsPerSubject, RunStreamsPerServer},
	application.AppStream: {AppStreamsPerSubject, AppStreamsPerServer},
}

var (
	errNoAccess    = errors.New("web: no credential")
	ErrStreamLimit = errors.New("web: too many open streams")
)

type streamSlot struct {
	kind application.StreamKind
	key  string
}

type streamCounts struct {
	sync.Mutex
	total map[application.StreamKind]int
	keys  map[streamSlot]int
}

func newStreamCounts() *streamCounts {
	return &streamCounts{total: map[application.StreamKind]int{}, keys: map[streamSlot]int{}}
}

func (s *streamCounts) acquire(kind application.StreamKind, key string) (func(), error) {
	limit, ok := streamCaps[kind]
	if !ok {
		return nil, ErrStreamLimit
	}
	slot := streamSlot{kind: kind, key: key}
	s.Lock()
	defer s.Unlock()
	if s.total[kind] >= limit.server || s.keys[slot] >= limit.subject {
		return nil, ErrStreamLimit
	}
	s.total[kind]++
	s.keys[slot]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.Lock()
			defer s.Unlock()
			s.total[kind]--
			if s.keys[slot]--; s.keys[slot] == 0 {
				delete(s.keys, slot)
			}
		})
	}, nil
}

type streamAccessKey struct{}

type streamAccess struct {
	key     string
	check   func(context.Context) error
	streams *streamCounts
}

func (g *Guard) withStreamAccess(ctx context.Context, key string, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, streamAccessKey{}, streamAccess{key: key, check: check, streams: g.streams})
}

func OpenStream(ctx context.Context, kind application.StreamKind) (release func(), check func(context.Context) error, err error) {
	a, ok := ctx.Value(streamAccessKey{}).(streamAccess)
	if !ok || a.streams == nil {
		return nil, nil, errNoAccess
	}
	release, err = a.streams.acquire(kind, a.key)
	if err != nil {
		return nil, nil, err
	}
	return release, a.check, nil
}

func (g *Guard) sessionAccess(r *http.Request, who Identity, browser string) context.Context {
	key := string(who.Source) + ":browser:" + tokenKey(browser)
	if who.Source == SourceOIDC {
		key = string(who.Source) + ":" + tokenKey(cookieValue(r, sessionCookie, g.secure(r)))
	}
	clone := r.Clone(context.WithoutCancel(r.Context()))
	return g.withStreamAccess(r.Context(), key, func(ctx context.Context) error {
		_, err := g.identify(clone.WithContext(ctx))
		return err
	})
}
