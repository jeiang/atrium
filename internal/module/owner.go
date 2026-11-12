package module

import (
	"context"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

type OwnerDisabled struct{ Module string }

func (e *OwnerDisabled) Error() string {
	return "the " + e.Module + " module was disabled; start the action again"
}

func (*OwnerDisabled) MutationRefused() {}

func Require(ctx context.Context, owner string, generation uint64) error {
	s := SetFrom(ctx)
	if s == nil {
		return &OwnerDisabled{Module: owner}
	}
	n := s.Current()
	if n == nil || !n.Enabled(owner) || n.Generation(owner) != generation {
		return &OwnerDisabled{Module: owner}
	}
	return nil
}

func BindOwner(ctx context.Context, owner string) (context.Context, error) {
	s := SetFrom(ctx)
	if s == nil || s.Current() == nil {
		return ctx, &OwnerDisabled{Module: owner}
	}
	generation := s.Current().Generation(owner)
	if err := Require(ctx, owner, generation); err != nil {
		return ctx, err
	}
	return ledger.WithAdmission(ctx, func() error { return Require(ctx, owner, generation) }), nil
}

func bindOwner(ctx context.Context, owner string, q db.Querier) (context.Context, error) {
	if set := SetFrom(ctx); set != nil && set.Current() == nil {
		snapshot, err := set.Evaluate(ctx, q)
		if err != nil {
			return ctx, err
		}
		set.cur.CompareAndSwap(nil, snapshot)
	}
	return BindOwner(ctx, owner)
}
