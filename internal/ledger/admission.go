package ledger

import "context"

type admissionKey struct{}
type writeStartKey struct{}

func WithAdmission(ctx context.Context, check func() error) context.Context {
	previous, _ := ctx.Value(admissionKey{}).(func() error)
	if previous != nil {
		next := check
		check = func() error {
			if err := previous(); err != nil {
				return err
			}
			return next()
		}
	}
	return context.WithValue(ctx, admissionKey{}, check)
}

func CheckAdmission(ctx context.Context) error {
	if check, ok := ctx.Value(admissionKey{}).(func() error); ok {
		return check()
	}
	return nil
}

func BeforeWrite(ctx context.Context) error {
	if start, ok := ctx.Value(writeStartKey{}).(func() error); ok {
		return start()
	}
	return nil
}
