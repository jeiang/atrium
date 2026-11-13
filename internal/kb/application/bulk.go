package application

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/application"
)

const (
	ConfirmSelectedName application.Name = "knowledge.confirm-selected"
	ConfirmMatchingName application.Name = "knowledge.confirm-matching"
)

type SelectedInput struct {
	IDs []int64
}

func DescribeConfirmSelected() application.Definition {
	return describeOperator[SelectedInput, BatchResult](ConfirmSelectedName, application.LocalUndoable)
}

func DescribeConfirmMatching() application.Definition {
	return describeOperator[ListInput, BatchResult](ConfirmMatchingName, application.LocalUndoable)
}

func (s *Service) batch(ids []int64, untilRefused bool, one func(int64) (application.Change[Changed], error)) (application.Change[BatchResult], error) {
	var out application.Change[BatchResult]
	for _, id := range ids {
		changed, err := one(id)
		var e *application.Error
		switch {
		case err == nil:
			out.Data.Committed = append(out.Data.Committed, BatchItem{ID: id})
			out.EffectIDs = append(out.EffectIDs, changed.EffectIDs...)
		case errors.As(err, &e) && e.Code != application.Internal && e.Code != application.Canceled:
			out.Data.Refused = append(out.Data.Refused, BatchItem{ID: id, Code: e.Code, Message: e.Error()})
			if untilRefused {
				return out, nil
			}
		default:
			failed := application.Classify(err)
			out.Data.Failed = append(out.Data.Failed, BatchItem{ID: id, Code: failed.Code, Message: failed.Error()})
			return out, err
		}
	}
	return out, nil
}

func (s *Service) ConfirmSelected(ctx context.Context, in SelectedInput) (application.Change[BatchResult], error) {
	if len(in.IDs) == 0 {
		return application.Change[BatchResult]{}, s.reject(ctx, ConfirmSelectedName, 0, invalid("id", "Select at least one record."))
	}
	return s.batch(in.IDs, true, func(id int64) (application.Change[Changed], error) { return s.confirm(ctx, ConfirmSelectedName, id) })
}

func (s *Service) ConfirmMatching(ctx context.Context, in ListInput) (application.Change[BatchResult], error) {
	in, err := in.Normalize()
	if err == nil && in.State != "unconfirmed" && in.State != "imported" {
		err = invalid("state", "Choose the unconfirmed or imported filter first.")
	}
	if err != nil {
		return application.Change[BatchResult]{}, s.reject(ctx, ConfirmMatchingName, 0, err)
	}
	found, err := s.List(ctx, in)
	if err != nil {
		return application.Change[BatchResult]{}, err
	}
	ids := make([]int64, 0, len(found.Data.Rows))
	for _, row := range found.Data.Rows {
		if !row.Confirmed {
			ids = append(ids, row.ID)
		}
	}
	return s.batch(ids, false, func(id int64) (application.Change[Changed], error) { return s.confirm(ctx, ConfirmMatchingName, id) })
}
