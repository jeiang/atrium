package settings

import "context"

type registryKey struct{}

func With(ctx context.Context, r *Registry) context.Context {
	return context.WithValue(ctx, registryKey{}, r)
}

func From(ctx context.Context) *Registry {
	r, _ := ctx.Value(registryKey{}).(*Registry)
	if r == nil {
		panic("settings: no registry in context")
	}
	return r
}
