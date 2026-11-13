package application

import "context"

type Invocation struct {
	ThreadID    int64
	RunID       int64
	JobRunID    int64
	Fingerprint string
}

type invocationKey struct{}

func WithInvocation(ctx context.Context, inv Invocation) context.Context {
	return context.WithValue(ctx, invocationKey{}, inv)
}

func InvocationOf(ctx context.Context) (Invocation, bool) {
	inv, ok := ctx.Value(invocationKey{}).(Invocation)
	return inv, ok
}
