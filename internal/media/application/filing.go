package application

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/media"
)

const (
	FilingPreviewName application.Name = "media.filing-preview"
	FileItemName      application.Name = "media.file-item"
	IgnoreName        application.Name = "media.ignore"
)

type FilingInput struct {
	ID   string
	Form map[string]string
}

type FilingPreview struct {
	Item  media.Item
	View  media.View
	Ops   []media.Op
	Error string
}

func DescribeFilingPreview() application.Definition {
	return application.Describe[FilingInput, FilingPreview](FilingPreviewName, application.Query,
		[]application.Authority{application.Operator}, application.NoEffect, false)
}

func (s *Service) FilingPreview(ctx context.Context, in FilingInput) (application.Read[FilingPreview], error) {
	set, root, item, err := s.find(ctx, in.ID)
	if err != nil {
		return application.Read[FilingPreview]{}, err
	}
	out := preview(set, root, item, in.Form)
	rev, err := s.revision(ctx, string(FilingPreviewName), in, out)
	if err != nil {
		return application.Read[FilingPreview]{}, err
	}
	return application.Read[FilingPreview]{Revision: rev, Data: out}, nil
}

func preview(set media.Settings, root string, item media.Item, form map[string]string) FilingPreview {
	plan := set.Plan(set.Edited(item, root, form), root)
	return FilingPreview{Item: item, View: plan.View, Ops: plan.Ops, Error: plan.Error}
}

type Filed struct {
	Item  media.Item
	Dests []string
}

func DescribeFileItem() application.Definition {
	return application.Describe[FilingInput, Filed](FileItemName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) FileItem(ctx context.Context, in FilingInput) (application.Change[Filed], error) {
	out, err := s.fileItem(ctx, in)
	id, err := s.record(ctx, FileItemName, in.ID, map[string]any{"placed": len(out.Dests)}, err)
	if err != nil {
		return application.Change[Filed]{}, err
	}
	return application.Change[Filed]{Data: out, AuditID: id}, nil
}

func (s *Service) fileItem(ctx context.Context, in FilingInput) (Filed, error) {
	set, root, item, err := s.find(ctx, in.ID)
	if err != nil {
		return Filed{}, err
	}
	plan := preview(set, root, item, in.Form)
	if plan.Error != "" {
		return Filed{}, invalid("plan", plan.Error)
	}
	if err := set.Apply(ctx, s.d.Ledger, set, item, plan.Ops, root); err != nil {
		var partial *media.PartialError
		if errors.As(err, &partial) {
			return Filed{Item: item, Dests: partial.Placed}, classify(err)
		}
		return Filed{}, classifyFiling(err)
	}
	dests := make([]string, len(plan.Ops))
	for i, op := range plan.Ops {
		dests[i] = op.Dst
	}
	return Filed{Item: item, Dests: dests}, nil
}

type IgnoreInput struct {
	ID string
}

type Ignored struct {
	ID string
}

func DescribeIgnore() application.Definition {
	return application.Describe[IgnoreInput, Ignored](IgnoreName, application.Command,
		[]application.Authority{application.Operator}, application.External, true)
}

func (s *Service) Ignore(ctx context.Context, in IgnoreInput) (application.Change[Ignored], error) {
	err := s.ignore(ctx, in)
	id, err := s.record(ctx, IgnoreName, in.ID, nil, err)
	if err != nil {
		return application.Change[Ignored]{}, err
	}
	return application.Change[Ignored]{Data: Ignored(in), AuditID: id}, nil
}

func (s *Service) ignore(ctx context.Context, in IgnoreInput) error {
	if in.ID == "" {
		return invalid("id", "Field required")
	}
	set, _, item, err := s.find(ctx, in.ID)
	if err != nil {
		return err
	}
	return classifyFiling(media.Ignore(ctx, s.d.Ledger, set, item))
}

func (s *Service) find(ctx context.Context, id string) (media.Settings, string, media.Item, error) {
	set, err := s.settings(ctx)
	if err != nil {
		return set, "", media.Item{}, err
	}
	client, err := set.Qbit()
	if err != nil {
		return set, "", media.Item{}, classify(err)
	}
	root, err := set.NeedRoot()
	if err != nil {
		return set, "", media.Item{}, classify(err)
	}
	item, err := set.Find(ctx, client, root, id)
	return set, root, item, classify(err)
}

func classifyFiling(err error) error {
	var partial *media.PartialError
	if errors.As(err, &partial) {
		return classify(err)
	}
	var plan media.PlanError
	if errors.As(err, &plan) {
		return &application.Error{Code: application.Invalid, Message: err.Error(), Cause: err}
	}
	return classifyWrite(err)
}
