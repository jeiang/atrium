package web

import (
	"context"
	"errors"
	"net/http"
)

type streamAccessKey struct{}

type streamAccess struct {
	key   string
	check func(context.Context) error
}

var errNoAccess = errors.New("web: no credential")

func withStreamAccess(ctx context.Context, key string, check func(context.Context) error) context.Context {
	return context.WithValue(ctx, streamAccessKey{}, streamAccess{key: key, check: check})
}

func StreamCredential(ctx context.Context) (string, func(context.Context) error) {
	a, ok := ctx.Value(streamAccessKey{}).(streamAccess)
	if !ok {
		return "", func(context.Context) error { return errNoAccess }
	}
	return a.key, a.check
}

func (g *Guard) sessionAccess(r *http.Request, who Identity, browser string) context.Context {
	key := string(who.Source) + ":browser:" + tokenKey(browser)
	if who.Source == SourceOIDC {
		key = string(who.Source) + ":" + tokenKey(cookieValue(r, sessionCookie, g.secure(r)))
	}
	clone := r.Clone(context.WithoutCancel(r.Context()))
	return withStreamAccess(r.Context(), key, func(ctx context.Context) error {
		_, err := g.identify(clone.WithContext(ctx))
		return err
	})
}
